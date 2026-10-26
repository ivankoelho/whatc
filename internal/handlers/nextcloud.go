package handlers

import (
	"context"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm/clause"
)

// Nextcloud, one server per organization. The admin registers its address once;
// each person connects with their own app password (created in Nextcloud, revocable
// there) and picks files of their own. A chosen file is fetched by the server over
// WebDAV and enters the chat through the SAME upload path as a file from the
// computer, so type, size and access rules are identical. Passwords are stored
// encrypted and never returned.

const (
	nextcloudTimeout    = 20 * time.Second
	nextcloudMaxListing = 500
	nextcloudMaxXML     = 4 << 20
)

var (
	errNextcloudNotConfigured = &chatHTTPError{http.StatusConflict, "Nextcloud is not set up for this organization", "nextcloud_not_configured"}
	errNextcloudNotConnected  = &chatHTTPError{http.StatusConflict, "Connect your Nextcloud account first", "nextcloud_not_connected"}
	errNextcloudAuth          = &chatHTTPError{http.StatusUnauthorized, "Nextcloud did not accept the user or app password", "nextcloud_auth"}
	errNextcloudUnreachable   = &chatHTTPError{http.StatusBadGateway, "Nextcloud could not be reached", "nextcloud_unreachable"}
	errNextcloudBadURL        = &chatHTTPError{http.StatusBadRequest, "Use the https address of your Nextcloud", "nextcloud_bad_url"}
	errNextcloudBadPath       = &chatHTTPError{http.StatusBadRequest, "Invalid path", "nextcloud_bad_path"}
	errNextcloudNotFound      = &chatHTTPError{http.StatusNotFound, "File not found in Nextcloud", "nextcloud_not_found"}
)

type nextcloudSettingsRequest struct {
	BaseURL string `json:"base_url"`
}

type nextcloudConnectRequest struct {
	Username    string `json:"username"`
	AppPassword string `json:"app_password"`
}

// NextcloudEntry is one file or folder of a listing.
type NextcloudEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mime  string `json:"mime,omitempty"`
}

// validNextcloudBase accepts an https address of a public host. Plain http and
// private addresses are for local development only (app.allow_private_nextcloud),
// so an admin cannot point the server at the internal network.
func validNextcloudBase(raw string, allowPrivate bool) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	if u.Scheme != "https" && !(allowPrivate && u.Scheme == "http") {
		return "", false
	}
	if !allowPrivate {
		host := u.Hostname()
		ips, lerr := net.LookupIP(host)
		if lerr != nil || len(ips) == 0 {
			return "", false
		}
		for _, ip := range ips {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
				return "", false
			}
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), true
}

// cleanNextcloudPath normalizes a user path to "/a/b" with no traversal.
func cleanNextcloudPath(p string) (string, bool) {
	if strings.ContainsAny(p, "\x00\\") {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", false
		}
	}
	c := path.Clean("/" + p)
	return c, true
}

func (a *App) nextcloudClient(base string) *http.Client {
	baseURL, _ := url.Parse(base)
	return &http.Client{
		Timeout: nextcloudTimeout,
		// Never follow a redirect to another host: credentials would go with it.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if baseURL == nil || req.URL.Host != baseURL.Host || len(via) > 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

type nextcloudSession struct {
	base, user, pass string
}

// nextcloudFor returns the organization's server and the caller's credentials.
func (a *App) nextcloudFor(orgID, userID uuid.UUID) (*nextcloudSession, error) {
	var st models.NextcloudSettings
	if err := a.DB.Where("organization_id = ?", orgID).First(&st).Error; err != nil {
		return nil, errNextcloudNotConfigured
	}
	var cr models.NextcloudCredential
	if err := a.DB.Where("organization_id = ? AND user_id = ?", orgID, userID).First(&cr).Error; err != nil {
		return nil, errNextcloudNotConnected
	}
	pass, err := crypto.Decrypt(cr.AppPassword, a.Config.App.EncryptionKey)
	if err != nil {
		return nil, errNextcloudNotConnected
	}
	return &nextcloudSession{base: st.BaseURL, user: cr.Username, pass: pass}, nil
}

func (s *nextcloudSession) davURL(p string) string {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i := range segs {
		segs[i] = url.PathEscape(segs[i])
	}
	full := s.base + "/remote.php/dav/files/" + url.PathEscape(s.user) + "/"
	if p != "/" && p != "" {
		full += strings.Join(segs, "/")
	}
	return full
}

func (a *App) nextcloudDo(ctx context.Context, s *nextcloudSession, method, p string, hdr map[string]string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.davURL(p), body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(s.user, s.pass)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return a.nextcloudClient(s.base).Do(req)
}

type davMultistatus struct {
	Responses []struct {
		Href  string `xml:"href"`
		Props []struct {
			ResourceType struct {
				Collection *struct{} `xml:"collection"`
			} `xml:"resourcetype"`
			ContentLength int64  `xml:"getcontentlength"`
			ContentType   string `xml:"getcontenttype"`
		} `xml:"propstat>prop"`
	} `xml:"response"`
}

func (a *App) listNextcloud(ctx context.Context, s *nextcloudSession, p string) ([]NextcloudEntry, error) {
	resp, err := a.nextcloudDo(ctx, s, "PROPFIND", p, map[string]string{"Depth": "1", "Content-Type": "application/xml"},
		strings.NewReader(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:getcontentlength/><d:getcontenttype/></d:prop></d:propfind>`))
	if err != nil {
		return nil, errNextcloudUnreachable
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, errNextcloudAuth
	case resp.StatusCode == http.StatusNotFound:
		return nil, errNextcloudNotFound
	case resp.StatusCode != http.StatusMultiStatus:
		return nil, errNextcloudUnreachable
	}
	var ms davMultistatus
	if err := xml.NewDecoder(io.LimitReader(resp.Body, nextcloudMaxXML)).Decode(&ms); err != nil {
		return nil, errNextcloudUnreachable
	}
	baseURL, _ := url.Parse(s.base)
	prefix := ""
	if baseURL != nil {
		prefix = strings.TrimRight(baseURL.Path, "/")
	}
	self := strings.TrimRight(path.Clean("/"+strings.Trim(p, "/")), "/")
	var out []NextcloudEntry
	for i, r := range ms.Responses {
		if i == 0 { // the folder itself
			continue
		}
		href, uerr := url.PathUnescape(r.Href)
		if uerr != nil {
			continue
		}
		rel := strings.TrimPrefix(href, prefix)
		rel = strings.TrimPrefix(rel, "/remote.php/dav/files/"+s.user)
		rel = "/" + strings.Trim(rel, "/")
		if rel == self || rel == "/" {
			continue
		}
		e := NextcloudEntry{Name: path.Base(rel), Path: rel}
		if len(r.Props) > 0 {
			pr := r.Props[0]
			e.IsDir = pr.ResourceType.Collection != nil
			e.Size, e.Mime = pr.ContentLength, pr.ContentType
		}
		out = append(out, e)
		if len(out) >= nextcloudMaxListing {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// GetNextcloudStatus tells the UI what to offer: nothing, "ask the admin", "connect", or the picker.
func (a *App) GetNextcloudStatus(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionRead)
	if err != nil {
		return nil
	}
	out := map[string]any{"configured": false, "connected": false}
	var st models.NextcloudSettings
	if a.DB.Where("organization_id = ?", orgID).First(&st).Error == nil {
		out["configured"], out["base_url"] = true, st.BaseURL
		var cr models.NextcloudCredential
		if a.DB.Where("organization_id = ? AND user_id = ?", orgID, userID).First(&cr).Error == nil {
			out["connected"], out["username"] = true, cr.Username
		}
	}
	out["can_configure"] = a.HasPermission(userID, models.ResourceSettingsGeneral, models.ActionWrite, orgID)
	return r.SendEnvelope(out)
}

// PutNextcloudSettings registers (or changes) the organization's server. Admin only.
func (a *App) PutNextcloudSettings(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req nextcloudSettingsRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	base, ok := validNextcloudBase(req.BaseURL, a.Config.App.AllowPrivateNextcloud)
	if !ok {
		return a.sendChatError(r, errNextcloudBadURL, "save the Nextcloud address")
	}
	st := models.NextcloudSettings{ID: uuid.New(), OrganizationID: orgID, BaseURL: base, UpdatedByID: &userID}
	if err := a.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "organization_id"}}, DoUpdates: clause.AssignmentColumns([]string{"base_url", "updated_by_id", "updated_at"}),
	}).Create(&st).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save", nil, "")
	}
	// A new address invalidates everyone's old credentials.
	a.DB.Where("organization_id = ?", orgID).Delete(&models.NextcloudCredential{})
	return r.SendEnvelope(map[string]any{"configured": true, "base_url": base})
}

// ConnectNextcloud checks the user's app password against the server, then stores it encrypted.
func (a *App) ConnectNextcloud(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req nextcloudConnectRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	user, pass := strings.TrimSpace(req.Username), req.AppPassword
	if user == "" || pass == "" || len(user) > 200 || len(pass) > 400 {
		return a.sendChatError(r, errNextcloudAuth, "connect to Nextcloud")
	}
	var st models.NextcloudSettings
	if a.DB.Where("organization_id = ?", orgID).First(&st).Error != nil {
		return a.sendChatError(r, errNextcloudNotConfigured, "connect to Nextcloud")
	}
	s := &nextcloudSession{base: st.BaseURL, user: user, pass: pass}
	if _, lerr := a.listNextcloud(context.Background(), s, "/"); lerr != nil {
		return a.sendChatError(r, lerr, "connect to Nextcloud")
	}
	enc, eerr := crypto.Encrypt(pass, a.Config.App.EncryptionKey)
	if eerr != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save", nil, "")
	}
	cr := models.NextcloudCredential{ID: uuid.New(), OrganizationID: orgID, UserID: userID, Username: user, AppPassword: enc}
	if err := a.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "organization_id"}, {Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"username", "app_password", "updated_at"}),
	}).Create(&cr).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save", nil, "")
	}
	return r.SendEnvelope(map[string]any{"connected": true, "username": user})
}

// DisconnectNextcloud forgets the caller's credentials.
func (a *App) DisconnectNextcloud(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionRead)
	if err != nil {
		return nil
	}
	a.DB.Where("organization_id = ? AND user_id = ?", orgID, userID).Delete(&models.NextcloudCredential{})
	return r.SendEnvelope(map[string]any{"connected": false})
}

// ListNextcloudFiles lists a folder of the caller's own Nextcloud.
func (a *App) ListNextcloudFiles(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	p, ok := cleanNextcloudPath(string(r.RequestCtx.QueryArgs().Peek("path")))
	if !ok {
		return a.sendChatError(r, errNextcloudBadPath, "list Nextcloud files")
	}
	s, serr := a.nextcloudFor(orgID, userID)
	if serr != nil {
		return a.sendChatError(r, serr, "list Nextcloud files")
	}
	entries, lerr := a.listNextcloud(context.Background(), s, p)
	if lerr != nil {
		return a.sendChatError(r, lerr, "list Nextcloud files")
	}
	return r.SendEnvelope(map[string]any{"path": p, "entries": entries})
}

// AttachNextcloudFile fetches one file of the caller's Nextcloud into the channel
// as a pending attachment, exactly like an upload from the computer.
func (a *App) AttachNextcloudFile(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceTeamChat, models.ActionWrite)
	if err != nil {
		return nil
	}
	ch, ok := a.chatChannelFor(r, userID, orgID)
	if !ok {
		return nil
	}
	if ch.ArchivedAt != nil {
		return a.sendChatError(r, errChatArchived, "attach the file")
	}
	if !a.chatUploadAllowed(userID) {
		return a.sendChatError(r, errChatRateLimited, "attach the file")
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	p, valid := cleanNextcloudPath(req.Path)
	if !valid || p == "/" {
		return a.sendChatError(r, errNextcloudBadPath, "attach the file")
	}
	s, serr := a.nextcloudFor(orgID, userID)
	if serr != nil {
		return a.sendChatError(r, serr, "attach the file")
	}
	resp, derr := a.nextcloudDo(context.Background(), s, http.MethodGet, p, nil, nil)
	if derr != nil {
		return a.sendChatError(r, errNextcloudUnreachable, "attach the file")
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return a.sendChatError(r, errNextcloudAuth, "attach the file")
	case resp.StatusCode == http.StatusNotFound:
		return a.sendChatError(r, errNextcloudNotFound, "attach the file")
	case resp.StatusCode != http.StatusOK:
		return a.sendChatError(r, errNextcloudUnreachable, "attach the file")
	}
	if resp.ContentLength > chatMaxFileBytes {
		return a.sendChatError(r, errChatFileTooLarge, "attach the file")
	}
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, chatMaxFileBytes+1))
	if rerr != nil {
		return a.sendChatError(r, errNextcloudUnreachable, "attach the file")
	}
	if len(data) > chatMaxFileBytes {
		return a.sendChatError(r, errChatFileTooLarge, "attach the file")
	}
	return a.finishChatUpload(r, orgID, userID, ch, data, path.Base(p), false, "")
}
