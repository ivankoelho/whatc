package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Viewing is not downloading: inline only for types the BYTES support, inert headers, and the antimalware
// verdict still decides first.

func TestPreview_WhatsAppMediaTypeComesFromTheBytes(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "media-reader", []string{"contacts:read", "conversations:view_all"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	get := func(rel string, content []byte) *fasthttp.RequestCtx {
		p := withStorageDir(t, app, rel, content)
		msg := makeMediaMessage(t, app, org.ID, contact.ID, p)
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "message_id", msg.ID.String())
		require.NoError(t, app.ServeMedia(req))
		return req.RequestCtx
	}
	head := func(c *fasthttp.RequestCtx, k string) string { return string(c.Response.Header.Peek(k)) }

	png := get("images/ok.png", tinyPNG(t))
	assert.Equal(t, "image/png", head(png, "Content-Type"))
	assert.Equal(t, "inline", head(png, "Content-Disposition"))
	assert.Equal(t, "nosniff", head(png, "X-Content-Type-Options"))
	assert.Contains(t, head(png, "Content-Security-Policy"), "default-src 'none'")

	// a file NAMED .png that is really HTML or SVG is never shown: opaque download
	for name, body := range map[string]string{
		"evil-html.png": "<html><script>alert(1)</script></html>",
		"evil-svg.png":  `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`,
		"evil-js.jpg":   "<script>alert(1)</script>",
	} {
		c := get("images/"+name, []byte(body))
		assert.Equal(t, "application/octet-stream", head(c, "Content-Type"), name)
		assert.Equal(t, "attachment", head(c, "Content-Disposition"), name)
		assert.Equal(t, "nosniff", head(c, "X-Content-Type-Options"), name)
	}
	// a PDF named .pdf is inline; a "PDF" that is not one is not
	pdf := get("documents/ok.pdf", pdfBytes())
	assert.Equal(t, "application/pdf", head(pdf, "Content-Type"))
	assert.Equal(t, "inline", head(pdf, "Content-Disposition"))
	fake := get("documents/fake.pdf", []byte("<html>not a pdf</html>"))
	assert.Equal(t, "application/octet-stream", head(fake, "Content-Type"))
	assert.Equal(t, "attachment", head(fake, "Content-Disposition"))
}

func TestPreview_ChatAttachmentPDFAndTextInlineOnRequestOnly_NeverWhenNotCleared(t *testing.T) {
	f := newPhaseA(t)
	f.app.Config.Storage.LocalPath = t.TempDir()
	code, pdf, raw := f.upload(t, f.owner, f.ch, "doc.pdf", pdfBytes())
	require.Equal(t, fasthttp.StatusOK, code, raw)
	code, txt, raw := f.upload(t, f.owner, f.ch, "nota.txt", []byte("linha 1\nlinha 2\n"))
	require.Equal(t, fasthttp.StatusOK, code, raw)

	get := func(id string, query string) *fasthttp.RequestCtx {
		req := testutil.NewGETRequest(t)
		if query != "" {
			req.RequestCtx.QueryArgs().Parse(query)
		}
		testutil.SetAuthContext(req, f.owner.OrganizationID, f.owner.ID)
		testutil.SetPathParam(req, "id", id)
		require.NoError(t, f.app.DownloadChatAttachment(req))
		_ = req.RequestCtx.Response.Body()
		return req.RequestCtx
	}
	disp := func(c *fasthttp.RequestCtx) string { return string(c.Response.Header.Peek("Content-Disposition")) }
	assert.Contains(t, disp(get(pdf.ID.String(), "")), "attachment", "download stays the default")
	inl := get(pdf.ID.String(), "inline=1")
	assert.Contains(t, disp(inl), "inline")
	assert.Equal(t, "application/pdf", string(inl.Response.Header.Peek("Content-Type")))
	assert.Equal(t, "nosniff", string(inl.Response.Header.Peek("X-Content-Type-Options")))
	tinl := get(txt.ID.String(), "inline=1")
	assert.Contains(t, disp(tinl), "inline")
	assert.Contains(t, string(tinl.Response.Header.Peek("Content-Type")), "text/plain")

	// a file that is not cleared has no preview either, whatever the query says
	scannerOf(f.app).set(func(s *fakeScanner) { s.down = true })
	code, pend, raw := f.upload(t, f.owner, f.ch, "pendente.pdf", append(pdfBytes(), 'p'))
	require.Equal(t, fasthttp.StatusOK, code, raw)
	assert.Equal(t, models.ScanError, pend.ScanStatus)
	assert.Equal(t, fasthttp.StatusLocked, get(pend.ID.String(), "inline=1").Response.StatusCode())
}

func TestPreview_InvoiceAttachmentInlineAndDownloadAreSeparate(t *testing.T) {
	app := newTestApp(t)
	app.Config.Storage.LocalPath = t.TempDir()
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	req := createOccurrenceWith(t, app, org.ID, user.ID, contact.ID, map[string]any{
		"documents": []map[string]any{{"type": "nf", "number": "1", "items": []map[string]any{{"description": "Piso"}}}},
	})
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var occ models.Occurrence
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).First(&occ).Error)
	var doc models.OccurrenceDocument
	require.NoError(t, app.DB.Where("occurrence_id = ?", occ.ID).First(&doc).Error)
	up := newMultipartUploadRequestWithContentType(t, "file", "nota.pdf", pdfBytes(), "application/pdf")
	testutil.SetAuthContext(up, org.ID, user.ID)
	testutil.SetPathParam(up, "id", occ.ID.String())
	testutil.SetPathParam(up, "docId", doc.ID.String())
	require.NoError(t, app.UploadOccurrenceDocumentAttachment(up))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(up))

	serve := func(query string) *fasthttp.RequestCtx {
		g := testutil.NewGETRequest(t)
		if query != "" {
			g.RequestCtx.QueryArgs().Parse(query)
		}
		testutil.SetAuthContext(g, org.ID, user.ID)
		testutil.SetPathParam(g, "id", occ.ID.String())
		testutil.SetPathParam(g, "docId", doc.ID.String())
		require.NoError(t, app.ServeOccurrenceDocumentAttachment(g))
		return g.RequestCtx
	}
	assert.Contains(t, string(serve("").Response.Header.Peek("Content-Disposition")), "inline")
	assert.Contains(t, string(serve("download=1").Response.Header.Peek("Content-Disposition")), "attachment")
	assert.Equal(t, "nosniff", string(serve("").Response.Header.Peek("X-Content-Type-Options")))
	assert.Contains(t, string(serve("").Response.Header.Peek("Content-Security-Policy")), "default-src 'none'")
}
