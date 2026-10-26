package handlers_test

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func tinyPNG(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))
	return b.Bytes()
}

// A stand-in for a Nextcloud server: WebDAV listing and download for one user.
func fakeNextcloud(t *testing.T, file []byte) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "alice" || pass != "app-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == "PROPFIND" && strings.HasSuffix(r.URL.Path, "/files/alice/"):
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">
<d:response><d:href>/remote.php/dav/files/alice/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>
<d:response><d:href>/remote.php/dav/files/alice/Docs/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>
<d:response><d:href>/remote.php/dav/files/alice/foto%20obra.png</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>120</d:getcontentlength><d:getcontenttype>image/png</d:getcontenttype></d:prop></d:propstat></d:response>
</d:multistatus>`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/files/alice/foto obra.png"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(file)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNextcloud_SetupConnectBrowseAndAttach(t *testing.T) {
	app := newTestApp(t)
	app.Config.App.AllowPrivateNextcloud = true
	app.Config.Storage.LocalPath = t.TempDir() // attachments must not land in the source tree
	srv := fakeNextcloud(t, tinyPNG(t))

	org := testutil.CreateTestOrganization(t, app.DB)
	admin := chatUser(t, app, org.ID, append([]string{"settings.general:read", "settings.general:write"}, chatUserKeys...))
	user := chatUser(t, app, org.ID, chatUserKeys)
	createTeamWithMember(t, app, org.ID, user.ID)
	ch := listChannels(t, app, org.ID, user.ID)[0]

	// Only an administrator registers the server, and only a public https address
	// unless private ones are allowed for development.
	code, _ := chatDo(t, app.PutNextcloudSettings, org.ID, user.ID, map[string]any{"base_url": srv.URL}, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	code, _ = chatDo(t, app.PutNextcloudSettings, org.ID, admin.ID, map[string]any{"base_url": "ftp://x"}, nil, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	app.Config.App.AllowPrivateNextcloud = false
	code, _ = chatDo(t, app.PutNextcloudSettings, org.ID, admin.ID, map[string]any{"base_url": srv.URL}, nil, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, code, "plain http on a local address is refused in production")
	app.Config.App.AllowPrivateNextcloud = true

	// Not set up yet: the picker has nothing to offer.
	_, raw := chatDo(t, app.GetNextcloudStatus, org.ID, user.ID, nil, nil, nil)
	assert.Contains(t, string(raw), `"configured":false`)
	code, _ = chatDo(t, app.ListNextcloudFiles, org.ID, user.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusConflict, code)

	code, _ = chatDo(t, app.PutNextcloudSettings, org.ID, admin.ID, map[string]any{"base_url": srv.URL}, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	_, raw = chatDo(t, app.GetNextcloudStatus, org.ID, user.ID, nil, nil, nil)
	assert.Contains(t, string(raw), `"configured":true`)
	assert.Contains(t, string(raw), `"connected":false`)

	// Connecting checks the password against the server and stores it encrypted.
	code, _ = chatDo(t, app.ConnectNextcloud, org.ID, user.ID, map[string]any{"username": "alice", "app_password": "wrong"}, nil, nil)
	assert.Equal(t, fasthttp.StatusUnauthorized, code)
	code, raw = chatDo(t, app.ConnectNextcloud, org.ID, user.ID, map[string]any{"username": "alice", "app_password": "app-secret"}, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	assert.NotContains(t, string(raw), "app-secret")
	var stored models.NextcloudCredential
	require.NoError(t, app.DB.Where("user_id = ?", user.ID).First(&stored).Error)
	assert.NotEqual(t, "app-secret", stored.AppPassword, "never stored in clear")

	// Browsing: folders first, a path cannot climb out.
	code, raw = chatDo(t, app.ListNextcloudFiles, org.ID, user.ID, nil, nil, map[string]string{"path": "/"})
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	body := string(raw)
	assert.Contains(t, body, `"name":"Docs"`)
	assert.Contains(t, body, `"name":"foto obra.png"`)
	assert.Less(t, strings.Index(body, `"name":"Docs"`), strings.Index(body, `"name":"foto obra.png"`))
	code, _ = chatDo(t, app.ListNextcloudFiles, org.ID, user.ID, nil, nil, map[string]string{"path": "/../etc"})
	assert.Equal(t, fasthttp.StatusBadRequest, code)

	// Attaching goes through the same pipeline as an upload from the computer.
	path := map[string]string{"id": ch.ID.String()}
	code, raw = chatDo(t, app.AttachNextcloudFile, org.ID, user.ID, map[string]any{"path": "/foto obra.png"}, path, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	att := decodeChat[handlers.ChatAttachmentResponse](t, raw)
	assert.Equal(t, "image", att.Kind)
	code, _ = chatDo(t, app.AttachNextcloudFile, org.ID, user.ID, map[string]any{"path": "/nao-existe.png"}, path, nil)
	assert.Equal(t, fasthttp.StatusNotFound, code)
	code, _ = chatDo(t, app.AttachNextcloudFile, org.ID, user.ID, map[string]any{"path": "/../x"}, path, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, code)

	// Someone outside the channel cannot attach into it, and credentials are per person.
	other := chatUser(t, app, org.ID, chatUserKeys)
	code, _ = chatDo(t, app.AttachNextcloudFile, org.ID, other.ID, map[string]any{"path": "/foto obra.png"}, path, nil)
	assert.Equal(t, fasthttp.StatusNotFound, code)
	code, _ = chatDo(t, app.ListNextcloudFiles, org.ID, other.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusConflict, code, "not connected: the colleague's login is not borrowed")

	// Another organization sees nothing of this server.
	foreign := newIsoWorld(t)
	fu := chatUser(t, app, foreign.org.ID, chatUserKeys)
	_, raw = chatDo(t, app.GetNextcloudStatus, foreign.org.ID, fu.ID, nil, nil, nil)
	assert.Contains(t, string(raw), `"configured":false`)

	// Production mode: the connection itself refuses a local address, whatever the saved URL says.
	app.Config.App.AllowPrivateNextcloud = false
	code, _ = chatDo(t, app.ListNextcloudFiles, org.ID, user.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusBadGateway, code, "dial-time check blocks loopback")
	app.Config.App.AllowPrivateNextcloud = true

	// Disconnecting forgets the password.
	code, _ = chatDo(t, app.DisconnectNextcloud, org.ID, user.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
	var n int64
	app.DB.Model(&models.NextcloudCredential{}).Where("user_id = ?", user.ID).Count(&n)
	assert.EqualValues(t, 0, n)
	_ = uuid.Nil
}
