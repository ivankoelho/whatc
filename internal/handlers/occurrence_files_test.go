package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

type fileEnvelope struct {
	File      handlers.OccurrenceFileResponse `json:"file"`
	Duplicate bool                            `json:"duplicate"`
}

func (w *chatWorld) fileUpload(t *testing.T, u *models.User, occID uuid.UUID, name string, data []byte, fields ...string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, name))
	h.Set("Content-Type", "application/octet-stream")
	part, err := mw.CreatePart(h)
	require.NoError(t, err)
	_, _ = part.Write(data)
	for i := 0; i+1 < len(fields); i += 2 {
		require.NoError(t, mw.WriteField(fields[i], fields[i+1]))
	}
	require.NoError(t, mw.Close())
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.SetContentType(mw.FormDataContentType())
	ctx.Request.SetBody(buf.Bytes())
	req := &fastglue.Request{RequestCtx: ctx}
	testutil.SetAuthContext(req, u.OrganizationID, u.ID)
	testutil.SetPathParam(req, "id", occID.String())
	require.NoError(t, w.app.UploadOccurrenceFile(req))
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
}

func (w *chatWorld) fileCall(t *testing.T, h func(*fastglue.Request) error, u *models.User, occID uuid.UUID, fid string, query string) (int, []byte, *fastglue.Request) {
	t.Helper()
	req := testutil.NewGETRequest(t)
	if query != "" {
		req.RequestCtx.QueryArgs().Parse(query)
	}
	testutil.SetAuthContext(req, u.OrganizationID, u.ID)
	testutil.SetPathParam(req, "id", occID.String())
	if fid != "" {
		testutil.SetPathParam(req, "fid", fid)
	}
	require.NoError(t, h(req))
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req), req
}

func uploaded(t *testing.T, raw []byte) fileEnvelope {
	var env fileEnvelope
	require.NoError(t, json.Unmarshal(raw, &struct {
		Data *fileEnvelope `json:"data"`
	}{&env}))
	return env
}

func newFilesWorld(t *testing.T) *chatWorld {
	w := newChatWorld(t)
	w.app.Config.Storage.LocalPath = t.TempDir()
	return w
}

// A case document is listed to everyone who can open the case, but its bytes follow the rule:
// those who work the case, the sender, and (if not restricted) everyone who can open it.
func TestOccurrenceFiles_UploadListDownloadAndDelete(t *testing.T) {
	w := newFilesWorld(t)
	w.run()

	code, raw := w.fileUpload(t, w.logPorto, w.occID, "orçamento.pdf", pdfBytes())
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	env := uploaded(t, raw)
	assert.Equal(t, "orçamento.pdf", env.File.Name)
	assert.Equal(t, "application/pdf", env.File.Mime)
	assert.True(t, env.File.Restricted, "restricted unless the sender says otherwise")
	fid := env.File.ID.String()

	// The SAC that forwarded the case lists it but cannot read it.
	_, raw, _ = w.fileCall(t, w.app.ListOccurrenceFiles, w.sac, w.occID, "", "")
	var list struct {
		Data struct {
			Files     []handlers.OccurrenceFileResponse `json:"files"`
			CanUpload bool                              `json:"can_upload"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &list))
	require.Len(t, list.Data.Files, 1)
	assert.False(t, list.Data.Files[0].CanDownload)
	assert.Equal(t, w.logPorto.FullName, list.Data.Files[0].UploaderName)
	code, _, _ = w.fileCall(t, w.app.DownloadOccurrenceFile, w.sac, w.occID, fid, "")
	assert.Equal(t, fasthttp.StatusForbidden, code)

	// Those who work the case can; the bytes come back with safe headers.
	for _, u := range []*models.User{w.logPorto, w.supPorto, w.supSAC} {
		code, body, req := w.fileCall(t, w.app.DownloadOccurrenceFile, u, w.occID, fid, "")
		require.Equal(t, fasthttp.StatusOK, code, u.FullName)
		assert.Equal(t, pdfBytes(), body)
		assert.Equal(t, "nosniff", string(req.RequestCtx.Response.Header.Peek("X-Content-Type-Options")))
		assert.Contains(t, string(req.RequestCtx.Response.Header.Peek("Content-Disposition")), "attachment")
	}
	// Inline preview only for PDFs and images.
	_, _, req := w.fileCall(t, w.app.DownloadOccurrenceFile, w.logPorto, w.occID, fid, "inline=1")
	assert.Contains(t, string(req.RequestCtx.Response.Header.Peek("Content-Disposition")), "inline")

	// Another store, another organization: the case itself is out of reach.
	code, _, _ = w.fileCall(t, w.app.DownloadOccurrenceFile, w.logOutra, w.occID, fid, "")
	assert.Equal(t, fasthttp.StatusForbidden, code)
	code, _, _ = w.fileCall(t, w.app.ListOccurrenceFiles, w.logOutra, w.occID, "", "")
	assert.Equal(t, fasthttp.StatusForbidden, code)
	other := newIsoWorld(t)
	foreign := chatUser(t, w.app, other.org.ID, caseSupervisorPerms)
	code, _, _ = w.fileCall(t, w.app.DownloadOccurrenceFile, foreign, w.occID, fid, "")
	assert.NotEqual(t, fasthttp.StatusOK, code)

	// IDOR: a file id is only valid under its own case; garbage ids are just "not found".
	otherCase := w.forward(t, nil)
	code, _, _ = w.fileCall(t, w.app.DownloadOccurrenceFile, w.logPorto, otherCase, fid, "")
	assert.Equal(t, fasthttp.StatusNotFound, code)
	code, _, _ = w.fileCall(t, w.app.DownloadOccurrenceFile, w.logPorto, w.occID, uuid.NewString(), "")
	assert.Equal(t, fasthttp.StatusNotFound, code)

	// A sender who marks a file "not restricted" lets everyone who opens the case read it.
	code, raw = w.fileUpload(t, w.logPorto, w.occID, "folha.pdf", append(append([]byte{}, pdfBytes()...), []byte("x")...), "restricted", "false")
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	open := uploaded(t, raw).File.ID.String()
	code, _, _ = w.fileCall(t, w.app.DownloadOccurrenceFile, w.sac, w.occID, open, "")
	assert.Equal(t, fasthttp.StatusOK, code)

	// History and audit: the send is a case event, the download an audit entry.
	var ev int64
	w.app.DB.Model(&models.OccurrenceEvent{}).Where("occurrence_id = ? AND type = ?", w.occID, models.OccurrenceEventFileAdded).Count(&ev)
	assert.EqualValues(t, 2, ev)
	require.Eventually(t, func() bool {
		var n int64
		w.app.DB.Model(&models.AuditLog{}).Where("resource_type = ? AND resource_id = ? AND action = ?", "occurrence_file", env.File.ID, models.AuditActionAccessed).Count(&n)
		return n >= 1
	}, 5e9, 25e6)

	// Removing: not by someone with no hand on it; by the sender or a supervisor. The bytes go away.
	code, _, _ = w.fileCall(t, w.app.DeleteOccurrenceFile, w.sac, w.occID, fid, "")
	assert.Equal(t, fasthttp.StatusForbidden, code)
	var stored models.OccurrenceFile
	require.NoError(t, w.app.DB.First(&stored, "id = ?", env.File.ID).Error)
	path := filepath.Join(w.app.Config.Storage.LocalPath, stored.StoragePath)
	_, statErr := os.Stat(path)
	require.NoError(t, statErr)
	assert.NotContains(t, stored.StoragePath, "orçamento", "the stored name carries nothing of the user's")
	code, _, _ = w.fileCall(t, w.app.DeleteOccurrenceFile, w.supPorto, w.occID, fid, "")
	assert.Equal(t, fasthttp.StatusOK, code)
	_, statErr = os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "the stored file is erased")
	code, _, _ = w.fileCall(t, w.app.DownloadOccurrenceFile, w.logPorto, w.occID, fid, "")
	assert.Equal(t, fasthttp.StatusNotFound, code)
	w.app.DB.Model(&models.OccurrenceEvent{}).Where("occurrence_id = ? AND type = ?", w.occID, models.OccurrenceEventFileRemoved).Count(&ev)
	assert.EqualValues(t, 1, ev)
}

func TestOccurrenceFiles_OnlyRealDocumentsAreAccepted(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	png := tinyPNG(t)
	ole := append(append([]byte{}, oleMagicForTest()...), make([]byte, 64)...)
	nfe := []byte(`<?xml version="1.0"?><nfeProc><NFe><infNFe Id="NFe35"><ide><nNF>1234</nNF></ide></infNFe></NFe></nfeProc>`)

	ok := []struct {
		name string
		data []byte
	}{{"a.pdf", pdfBytes()}, {"foto.png", png}, {"nf.xml", nfe}, {"antigo.doc", ole}, {"planilha.xls", ole}, {"notas.csv", []byte("a;b\n1;2\n")}}
	for _, c := range ok {
		code, raw := w.fileUpload(t, w.logPorto, w.occID, c.name, c.data)
		assert.Equal(t, fasthttp.StatusOK, code, "%s: %s", c.name, raw)
	}

	refused := []struct {
		name string
		data []byte
		why  string
	}{
		{"x.exe", []byte("MZ\x90\x00"), "executable"},
		{"a.pdf.exe", pdfBytes(), "double extension"},
		{"page.html", []byte("<html><script>alert(1)</script></html>"), "html"},
		{"logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`), "svg"},
		{"fake.pdf", png, "png bytes under a pdf name"},
		{"fake.png", pdfBytes(), "pdf bytes under a png name"},
		{"bomb.xml", []byte(`<?xml version="1.0"?><!DOCTYPE x [<!ENTITY a "aaaa">]><x>&a;</x>`), "xml with entities"},
		{"broken.xml", []byte(`<a><b></a>`), "malformed xml"},
		{"evil.doc", append(oleMagicForTest(), 0), "ole under a doc name is fine, but with .exe is not"},
		{"empty.pdf", nil, "empty"},
		{"voz.mp3", []byte("ID3\x03\x00\x00\x00\x00\x00\x00audio"), "audio is not a case document"},
	}
	for _, c := range refused {
		if c.name == "evil.doc" {
			continue // allowed above: legacy Word under its own extension
		}
		code, _ := w.fileUpload(t, w.logPorto, w.occID, c.name, c.data)
		assert.GreaterOrEqual(t, code, 400, c.why)
		assert.Less(t, code, 500, c.why)
	}
	// Legacy Office bytes under the wrong extension.
	code, _ := w.fileUpload(t, w.logPorto, w.occID, "x.pdf", ole)
	assert.GreaterOrEqual(t, code, 400)

	// Too big (the limit is configurable).
	w.app.Config.App.OccurrenceFileMaxMB = 1
	big := append(append([]byte{}, pdfBytes()...), bytes.Repeat([]byte("0"), 2<<20)...)
	code, _ = w.fileUpload(t, w.logPorto, w.occID, "grande.pdf", big)
	assert.Equal(t, fasthttp.StatusRequestEntityTooLarge, code)

	// A hostile name never reaches the disk path.
	w.app.Config.App.OccurrenceFileMaxMB = 15
	code, raw := w.fileUpload(t, w.logPorto, w.occID, "../../../etc/passwd.pdf", append(append([]byte{}, pdfBytes()...), 'z'))
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	assert.Equal(t, "passwd.pdf", uploaded(t, raw).File.Name)
	var stored models.OccurrenceFile
	require.NoError(t, w.app.DB.First(&stored, "id = ?", uploaded(t, raw).File.ID).Error)
	assert.NotContains(t, stored.StoragePath, "..")
}

func oleMagicForTest() []byte { return []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1} }

// Who may attach: those who work the case; a follower only with the right to comment.
func TestOccurrenceFiles_WhoMayAttach(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	code, _ := w.fileUpload(t, w.sac, w.occID, "a.pdf", pdfBytes())
	assert.Equal(t, fasthttp.StatusOK, code, "the SAC that forwarded the case follows it with the right to comment")
	code, _ = w.fileUpload(t, w.logOutra, w.occID, "a.pdf", pdfBytes())
	assert.Equal(t, fasthttp.StatusForbidden, code, "another store")
	noWrite := chatUser(t, w.app, w.org.ID, []string{"occurrences:read", "conversations:view_all"})
	code, _ = w.fileUpload(t, noWrite, w.occID, "a.pdf", pdfBytes())
	assert.Equal(t, fasthttp.StatusForbidden, code, "read-only permission")
	code, _ = w.fileUpload(t, w.logPorto, w.occID, "b.pdf", append(pdfBytes(), 'y'))
	assert.Equal(t, fasthttp.StatusOK, code)
}

// The same file sent twice in a row (double click, retried request) is stored once.
func TestOccurrenceFiles_RepeatedRequestDoesNotDuplicate(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	_, raw := w.fileUpload(t, w.logPorto, w.occID, "a.pdf", pdfBytes())
	first := uploaded(t, raw)
	_, raw = w.fileUpload(t, w.logPorto, w.occID, "a.pdf", pdfBytes())
	second := uploaded(t, raw)
	assert.True(t, second.Duplicate)
	assert.Equal(t, first.File.ID, second.File.ID)
	var n int64
	w.app.DB.Model(&models.OccurrenceFile{}).Where("occurrence_id = ?", w.occID).Count(&n)
	assert.EqualValues(t, 1, n)
}
