package handlers_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// File attachments and voice notes (spec 2026-10-17). Uses the Phase A fixture;
// every test gets its own storage directory.

func (f *phaseA) withStorage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	f.app.Config.Storage.LocalPath = dir
	return dir
}

func pngBytes(w, h int) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.Gray{Y: 200})
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func jpegBytes() []byte {
	var b bytes.Buffer
	_ = jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, 4, 4)), nil)
	return b.Bytes()
}

func gifBytes() []byte {
	var b bytes.Buffer
	_ = gif.Encode(&b, image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black, color.White}), nil)
	return b.Bytes()
}

func webpBytes() []byte { // a VP8X header declaring a 100x50 canvas
	d := make([]byte, 30)
	copy(d, "RIFF")
	copy(d[8:], "WEBPVP8X")
	d[24], d[27] = 99, 49
	return d
}

func pdfBytes() []byte { return []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n") }

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return b.Bytes()
}

func ooxml(t *testing.T, dir string, extra map[string][]byte) []byte {
	files := map[string][]byte{"[Content_Types].xml": []byte("<Types/>"), dir + "/document.xml": []byte("<x/>")}
	for k, v := range extra {
		files[k] = v
	}
	return zipOf(t, files)
}

func webmAudio() []byte {
	d := make([]byte, 64)
	copy(d, "\x1a\x45\xdf\xa3")
	copy(d[20:], "webm")
	return d
}

func m4aAudio() []byte {
	d := make([]byte, 32)
	copy(d[4:], "ftypM4A ")
	return d
}

// upload sends one multipart file through the real handler.
func (f *phaseA) upload(t *testing.T, u *models.User, ch *models.ChatChannel, name string, data []byte, fields ...string) (int, handlers.ChatAttachmentResponse, string) {
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
	testutil.SetPathParam(req, "id", ch.ID.String())
	require.NoError(t, f.app.UploadChatAttachment(req))
	code := testutil.GetResponseStatusCode(req)
	raw := testutil.GetResponseBody(req)
	if code != fasthttp.StatusOK {
		return code, handlers.ChatAttachmentResponse{}, string(raw)
	}
	return code, decodeChat[handlers.ChatAttachmentResponse](t, raw), ""
}

func (f *phaseA) download(t *testing.T, u *models.User, id uuid.UUID, rng string) *fastglue.Request {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, u.OrganizationID, u.ID)
	testutil.SetPathParam(req, "id", id.String())
	if rng != "" {
		testutil.SetHeader(req, "Range", rng)
	}
	require.NoError(t, f.app.DownloadChatAttachment(req))
	_ = req.RequestCtx.Response.Body() // drain the stream so the file is closed
	return req
}

func (f *phaseA) sendFiles(t *testing.T, u *models.User, ch *models.ChatChannel, body string, ids ...uuid.UUID) (int, handlers.ChatMessageResponse, string) {
	t.Helper()
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = id.String()
	}
	code, raw := chatDo(t, f.app.SendChatMessage, u.OrganizationID, u.ID,
		map[string]any{"body": body, "attachment_ids": strs}, map[string]string{"id": ch.ID.String()}, nil)
	if code != fasthttp.StatusOK {
		return code, handlers.ChatMessageResponse{}, string(raw)
	}
	return code, decodeChat[handlers.ChatMessageResponse](t, raw), ""
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func (f *phaseA) rows(t *testing.T) int64 {
	var n int64
	f.app.DB.Model(&models.ChatAttachment{}).Count(&n)
	return n
}

func mustUpload(t *testing.T, f *phaseA, u *models.User, ch *models.ChatChannel, name string, data []byte, fields ...string) handlers.ChatAttachmentResponse {
	t.Helper()
	code, a, body := f.upload(t, u, ch, name, data, fields...)
	require.Equal(t, fasthttp.StatusOK, code, body)
	return a
}

func TestAttachments_AcceptedTypes(t *testing.T) {
	f := newPhaseA(t)
	defer handlers.SetChatUploadsPerMinuteForTest(1000)()
	f.withStorage(t)
	cases := []struct {
		name   string
		data   []byte
		fields []string
		kind   string
		mime   string
	}{
		{"foto.png", pngBytes(10, 5), nil, "image", "image/png"},
		{"foto.JPG", jpegBytes(), nil, "image", "image/jpeg"},
		{"anim.gif", gifBytes(), nil, "image", "image/gif"},
		{"img.webp", webpBytes(), nil, "image", "image/webp"},
		{"contrato.pdf", pdfBytes(), nil, "file", "application/pdf"},
		{"notas.txt", []byte("olá, mundo\n"), nil, "file", "text/plain"},
		{"dados.csv", []byte("a,b\n1,2\n"), nil, "file", "text/csv"},
		{"doc.docx", ooxml(t, "word", nil), nil, "file", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"plan.xlsx", ooxml(t, "xl", nil), nil, "file", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"slides.pptx", ooxml(t, "ppt", nil), nil, "file", "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
		{"audio.webm", webmAudio(), []string{"kind", "audio", "duration_ms", "31000"}, "audio", "audio/webm"},
		{"audio.m4a", m4aAudio(), []string{"kind", "audio"}, "audio", "audio/mp4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := mustUpload(t, f, f.member, f.ch, c.name, c.data, c.fields...)
			assert.Equal(t, c.kind, a.Kind)
			assert.Equal(t, c.mime, a.Mime)
			assert.EqualValues(t, len(c.data), a.Size)
			if c.kind == "image" {
				require.NotNil(t, a.Width)
				assert.Greater(t, *a.Width, 0)
			}
		})
		f.app.DB.Where("uploader_id = ?", f.member.ID).Delete(&models.ChatAttachment{}) // stay under the pending cap
	}
	// The recording's declared duration is kept (clamped), informational only.
	a := mustUpload(t, f, f.member, f.ch, "a.webm", webmAudio(), "kind", "audio", "duration_ms", "999999999")
	require.NotNil(t, a.DurationMs)
	assert.Equal(t, 300000, *a.DurationMs)
}

func TestAttachments_RefusedTypes(t *testing.T) {
	f := newPhaseA(t)
	dir := f.withStorage(t)
	rowsBefore := f.rows(t)
	macroDocx := ooxml(t, "word", map[string][]byte{"word/vbaProject.bin": []byte("x")})
	macroTypes := zipOf(t, map[string][]byte{"[Content_Types].xml": []byte("<Types><Override ContentType='application/vnd.ms-word.document.macroEnabled.12'/></Types>"), "word/document.xml": []byte("<x/>")})
	cases := []struct {
		name   string
		file   string
		data   []byte
		fields []string
		status int
		code   string
	}{
		{"svg with script", "x.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), nil, 415, "file_type_not_allowed"},
		{"html named as png", "x.png", []byte("<html><script>alert(1)</script></html>"), nil, 415, "file_type_not_allowed"},
		{"html", "page.html", []byte("<html></html>"), nil, 415, "file_type_not_allowed"},
		{"windows executable", "setup.exe", []byte("MZ\x90\x00\x03"), nil, 415, "file_type_not_allowed"},
		{"double extension", "invoice.pdf.exe", pdfBytes(), nil, 415, "file_type_not_allowed"},
		{"plain zip", "a.zip", zipOf(t, map[string][]byte{"a.txt": []byte("x")}), nil, 415, "file_type_not_allowed"},
		{"macro docx (vbaProject)", "m.docx", macroDocx, nil, 415, "file_type_not_allowed"},
		{"macro docx (content types)", "m2.docx", macroTypes, nil, 415, "file_type_not_allowed"},
		{"unknown binary", "blob.bin", []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}, nil, 415, "file_type_not_allowed"},
		{"video container as a file", "v.webm", webmAudio(), nil, 415, "file_type_not_allowed"},
		{"png bytes, pdf name", "foto.pdf", pngBytes(2, 2), nil, 400, "invalid_file"},
		{"pdf bytes, png name", "scan.png", pdfBytes(), nil, 400, "invalid_file"},
		{"jpeg bytes, txt name", "a.txt", jpegBytes(), nil, 400, "invalid_file"},
		{"audio claimed, pdf sent", "a.pdf", pdfBytes(), []string{"kind", "audio"}, 400, "invalid_file"},
		{"empty", "empty.txt", []byte{}, nil, 400, "invalid_file"},
		{"text with NUL", "a.txt", []byte("abc\x00def"), nil, 400, "invalid_file"},
		{"image too wide", "wide.png", pngBytes(8001, 1), nil, 400, "invalid_file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, _, body := f.upload(t, f.member, f.ch, c.file, c.data, c.fields...)
			assert.Equal(t, c.status, code, body)
			assert.Contains(t, body, c.code)
		})
	}
	assert.Equal(t, rowsBefore, f.rows(t), "refused files leave no row")
	assert.Zero(t, countFiles(t, dir), "refused files leave nothing on disk")
}

func TestAttachments_SizeAndZipBombs(t *testing.T) {
	f := newPhaseA(t)
	dir := f.withStorage(t)

	big := bytes.Repeat([]byte("a"), 10<<20+1)
	code, _, body := f.upload(t, f.member, f.ch, "big.txt", big)
	assert.Equal(t, 413, code)
	assert.Contains(t, body, "file_too_large")

	exact := bytes.Repeat([]byte("a"), 10<<20)
	code, _, _ = f.upload(t, f.member, f.ch, "exact.txt", exact)
	assert.Equal(t, 200, code, "exactly the limit is accepted")
	var exactRow models.ChatAttachment
	require.NoError(t, f.app.DB.Order("created_at DESC").First(&exactRow).Error)
	require.NoError(t, os.Remove(filepath.Join(dir, exactRow.StoragePath)))
	f.app.DB.Delete(&exactRow)

	audio := bytes.Repeat([]byte{0}, 5<<20+1)
	copy(audio, webmAudio())
	code, _, _ = f.upload(t, f.member, f.ch, "long.webm", audio, "kind", "audio")
	assert.Equal(t, 400, code, "audio over 5 MB")

	many := map[string][]byte{"[Content_Types].xml": []byte("<Types/>")}
	for i := 0; i < 205; i++ {
		many[fmt.Sprintf("word/p%03d.xml", i)] = []byte("x")
	}
	code, _, _ = f.upload(t, f.member, f.ch, "many.docx", zipOf(t, many))
	assert.Equal(t, 400, code, "more than 200 entries")

	bomb := ooxml(t, "word", map[string][]byte{"word/big.bin": bytes.Repeat([]byte{0}, 51<<20)})
	code, _, _ = f.upload(t, f.member, f.ch, "bomb.docx", bomb)
	assert.Equal(t, 400, code, "more than 50 MB uncompressed")
	assert.Zero(t, countFiles(t, filepath.Join(dir)), "nothing left on disk after the refusals")
}

func TestAttachments_HostileNamesNeverReachTheDisk(t *testing.T) {
	f := newPhaseA(t)
	dir := f.withStorage(t)
	long := strings.Repeat("x", 300) + ".png"
	for _, name := range []string{"../../etc/evil.png", `..\..\win\evil.png`, "C:\\temp\\evil.png", "...hidden.png", "a\u202eGNP.png", long} {
		a := mustUpload(t, f, f.member, f.ch, name, pngBytes(2, 2))
		assert.NotContains(t, a.Name, "/")
		assert.NotContains(t, a.Name, `\`)
		assert.NotContains(t, a.Name, "\u202e")
		assert.False(t, strings.HasPrefix(a.Name, "."), a.Name)
		assert.LessOrEqual(t, len([]rune(a.Name)), 255)
		var row models.ChatAttachment
		require.NoError(t, f.app.DB.First(&row, "id = ?", a.ID).Error)
		assert.Equal(t, filepath.Join("team-chat", f.org.ID.String(), f.ch.ID.String(), a.ID.String()+".png"), row.StoragePath,
			"the path is built from ids only")
		f.app.DB.Delete(&row)
	}
	// Every file lives under team-chat/{org}/{channel}/ and nowhere else.
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			assert.True(t, strings.HasPrefix(p, filepath.Join(dir, "team-chat", f.org.ID.String(), f.ch.ID.String())), p)
		}
		return nil
	})
}

func TestAttachments_DownloadAccessMatrix(t *testing.T) {
	f := newPhaseA(t)
	f.withStorage(t)
	payload := pngBytes(7, 7)
	att := mustUpload(t, f, f.member, f.ch, "foto.png", payload)

	t.Run("pending is visible to its uploader only", func(t *testing.T) {
		assert.Equal(t, 200, testutil.GetResponseStatusCode(f.download(t, f.member, att.ID, "")))
		assert.Equal(t, 404, testutil.GetResponseStatusCode(f.download(t, f.owner, att.ID, "")), "another member")
	})

	code, msg, body := f.sendFiles(t, f.member, f.ch, "olha", att.ID)
	require.Equal(t, 200, code, body)
	require.Len(t, msg.Attachments, 1)

	t.Run("members download exactly what was sent", func(t *testing.T) {
		for _, u := range []*models.User{f.member, f.owner} {
			req := f.download(t, u, att.ID, "")
			assert.Equal(t, 200, testutil.GetResponseStatusCode(req))
			assert.Equal(t, payload, testutil.GetResponseBody(req))
		}
	})
	t.Run("everyone else gets 404, never 403", func(t *testing.T) {
		assert.Equal(t, 404, testutil.GetResponseStatusCode(f.download(t, f.spare, att.ID, "")), "not in the channel")
		assert.Equal(t, 404, testutil.GetResponseStatusCode(f.download(t, f.foreign, att.ID, "")), "another organization")
		assert.Equal(t, 404, testutil.GetResponseStatusCode(f.download(t, f.member, uuid.New(), "")), "unknown id")
	})
	t.Run("without team_chat permission", func(t *testing.T) {
		assert.Equal(t, 403, testutil.GetResponseStatusCode(f.download(t, f.noPerm, att.ID, "")))
	})
	t.Run("losing access to the channel takes the file away", func(t *testing.T) {
		require.NoError(t, f.app.DB.Where("channel_id = ? AND user_id = ?", f.ch.ID, f.member.ID).Delete(&models.ChatChannelMember{}).Error)
		assert.Equal(t, 404, testutil.GetResponseStatusCode(f.download(t, f.member, att.ID, "")))
		require.NoError(t, f.app.DB.Create(&models.ChatChannelMember{OrganizationID: f.org.ID, ChannelID: f.ch.ID, UserID: f.member.ID, Role: "member"}).Error)
	})
	t.Run("an archived channel stays readable", func(t *testing.T) {
		f.archiveNow(t, f.ch)
		assert.Equal(t, 200, testutil.GetResponseStatusCode(f.download(t, f.owner, att.ID, "")))
	})
	t.Run("deleting the message closes the file at once", func(t *testing.T) {
		ch := f.freeChannel(t, "other", f.owner, f.member)
		a2 := mustUpload(t, f, f.member, ch, "foto.png", payload)
		_, m2, _ := f.sendFiles(t, f.member, ch, "", a2.ID)
		code, _ := chatDo(t, f.app.DeleteChatMessage, f.org.ID, f.member.ID, nil, map[string]string{"id": m2.ID.String()}, nil)
		require.Equal(t, 200, code)
		assert.Equal(t, 404, testutil.GetResponseStatusCode(f.download(t, f.member, a2.ID, "")))
		for _, m := range f.page(t, f.owner, ch) {
			assert.NotEqual(t, m2.ID, m.ID)
		}
	})
}

func TestAttachments_ResponseHeaders(t *testing.T) {
	f := newPhaseA(t)
	f.withStorage(t)
	polyglot := append(gifBytes(), []byte("<script>alert(1)</script>")...)
	cases := []struct {
		name, mime, disp string
		data             []byte
		fields           []string
	}{
		{"foto.png", "image/png", "inline", pngBytes(3, 3), nil},
		{"evil.gif", "image/gif", "inline", polyglot, nil},
		{"audio.webm", "audio/webm", "inline", webmAudio(), []string{"kind", "audio"}},
		{"contrato.pdf", "application/pdf", "attachment", pdfBytes(), nil},
		{"notas.txt", "text/plain", "attachment", []byte("oi"), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := mustUpload(t, f, f.member, f.ch, c.name, c.data, c.fields...)
			require.Equal(t, 200, func() int { code, _, _ := f.sendFiles(t, f.member, f.ch, "", a.ID); return code }())
			req := f.download(t, f.owner, a.ID, "")
			h := &req.RequestCtx.Response.Header
			assert.Equal(t, c.mime, string(h.Peek("Content-Type")), "the detected type, not the browser's")
			assert.Equal(t, "nosniff", string(h.Peek("X-Content-Type-Options")))
			assert.Equal(t, "default-src 'none'; sandbox", string(h.Peek("Content-Security-Policy")))
			assert.Equal(t, "same-origin", string(h.Peek("Cross-Origin-Resource-Policy")))
			assert.True(t, strings.HasPrefix(string(h.Peek("Content-Disposition")), c.disp+";"), string(h.Peek("Content-Disposition")))
			assert.Equal(t, "bytes", string(h.Peek("Accept-Ranges")))
			assert.Equal(t, c.data, testutil.GetResponseBody(req))
		})
	}
	t.Run("a name with CR/LF cannot split the header", func(t *testing.T) {
		a := mustUpload(t, f, f.member, f.ch, "ok.png", pngBytes(2, 2))
		require.NoError(t, f.app.DB.Model(&models.ChatAttachment{}).Where("id = ?", a.ID).Update("name", "a\r\nSet-Cookie: x=1.png").Error)
		f.sendFiles(t, f.member, f.ch, "", a.ID)
		req := f.download(t, f.owner, a.ID, "")
		disp := string(req.RequestCtx.Response.Header.Peek("Content-Disposition"))
		assert.NotContains(t, disp, "\r")
		assert.NotContains(t, disp, "\n")
		assert.Contains(t, disp, "%0D%0A")
	})
}

func TestAttachments_RangeRequests(t *testing.T) {
	f := newPhaseA(t)
	f.withStorage(t)
	data := append(webmAudio(), bytes.Repeat([]byte("0123456789"), 20)...) // 264 bytes
	a := mustUpload(t, f, f.member, f.ch, "a.webm", data, "kind", "audio")
	f.sendFiles(t, f.member, f.ch, "", a.ID)

	req := f.download(t, f.owner, a.ID, "bytes=10-19")
	assert.Equal(t, 206, testutil.GetResponseStatusCode(req))
	assert.Equal(t, data[10:20], testutil.GetResponseBody(req))
	assert.Equal(t, fmt.Sprintf("bytes 10-19/%d", len(data)), string(req.RequestCtx.Response.Header.Peek("Content-Range")))

	req = f.download(t, f.owner, a.ID, "bytes=-5")
	assert.Equal(t, 206, testutil.GetResponseStatusCode(req))
	assert.Equal(t, data[len(data)-5:], testutil.GetResponseBody(req))

	req = f.download(t, f.owner, a.ID, "bytes=250-")
	assert.Equal(t, data[250:], testutil.GetResponseBody(req))

	for _, bad := range []string{"bytes=9999-", "bytes=20-10", "bytes=abc"} {
		assert.Equal(t, 416, testutil.GetResponseStatusCode(f.download(t, f.owner, a.ID, bad)), bad)
	}
	req = f.download(t, f.owner, a.ID, "bytes=0-1,5-6")
	assert.Equal(t, 200, testutil.GetResponseStatusCode(req), "several ranges: the whole file")
	assert.Equal(t, data, testutil.GetResponseBody(req))
}

func TestAttachments_SendingRules(t *testing.T) {
	f := newPhaseA(t)
	defer handlers.SetChatUploadsPerMinuteForTest(1000)()
	dir := f.withStorage(t)
	up := func(u *models.User, ch *models.ChatChannel) uuid.UUID {
		return mustUpload(t, f, u, ch, "foto.png", pngBytes(2, 2)).ID
	}
	msgCount := func(ch *models.ChatChannel) int64 {
		var n int64
		f.app.DB.Model(&models.ChatMessage{}).Where("channel_id = ? AND kind = ?", ch.ID, models.ChatMessageKindUser).Count(&n)
		return n
	}

	t.Run("files without text are a valid message; neither is not", func(t *testing.T) {
		code, m, _ := f.sendFiles(t, f.member, f.ch, "", up(f.member, f.ch))
		assert.Equal(t, 200, code)
		assert.Len(t, m.Attachments, 1)
		assert.Empty(t, m.Body)
		code, _, _ = f.sendFiles(t, f.member, f.ch, "")
		assert.Equal(t, 400, code)
		code, _, _ = f.sendFiles(t, f.member, f.ch, "   ")
		assert.Equal(t, 400, code)
	})

	t.Run("six attachments are too many", func(t *testing.T) {
		ids := make([]uuid.UUID, 6)
		for i := range ids {
			ids[i] = up(f.member, f.ch)
		}
		code, _, body := f.sendFiles(t, f.member, f.ch, "x", ids...)
		assert.Equal(t, 400, code)
		assert.Contains(t, body, "too_many_attachments")
		code, m, _ := f.sendFiles(t, f.member, f.ch, "five", ids[:5]...)
		assert.Equal(t, 200, code)
		assert.Len(t, m.Attachments, 5)
	})

	t.Run("an id that is not yours, not here, used or unknown aborts the whole send", func(t *testing.T) {
		mine := up(f.member, f.ch)
		theirs := up(f.owner, f.ch)
		other := f.freeChannel(t, "other", f.member, f.owner)
		inOther := up(f.member, other)
		_, used, _ := f.sendFiles(t, f.member, f.ch, "first", up(f.member, f.ch))
		before := msgCount(f.ch)
		for name, ids := range map[string][]uuid.UUID{
			"someone else's": {mine, theirs}, "another channel": {mine, inOther},
			"already used": {mine, used.Attachments[0].ID}, "unknown": {mine, uuid.New()}, "duplicated": {mine, mine},
		} {
			code, _, body := f.sendFiles(t, f.member, f.ch, "x", ids...)
			assert.Equal(t, 400, code, name)
			assert.Contains(t, body, "attachment_gone", name)
		}
		assert.Equal(t, before, msgCount(f.ch), "no message was created by any failed send")
		code, _, _ := f.sendFiles(t, f.member, f.ch, "now", mine)
		assert.Equal(t, 200, code, "and the good one is still usable")
	})

	t.Run("a failed send keeps the uploads for another try", func(t *testing.T) {
		id := up(f.member, f.ch)
		code, _, _ := f.sendFiles(t, f.readOnl, f.ch, "x", id)
		assert.NotEqual(t, 200, code)
		code, _, _ = f.sendFiles(t, f.member, f.ch, "retry", id)
		assert.Equal(t, 200, code)
	})

	t.Run("editing keeps the files; the text may go empty only with files", func(t *testing.T) {
		_, m, _ := f.sendFiles(t, f.member, f.ch, "with file", up(f.member, f.ch))
		code, _ := chatDo(t, f.app.UpdateChatMessage, f.org.ID, f.member.ID, map[string]any{"body": ""}, map[string]string{"id": m.ID.String()}, nil)
		assert.Equal(t, 200, code)
		_, plain := sendMsg(t, f.app, f.org.ID, f.member.ID, f.ch.ID, "text only")
		code, _ = chatDo(t, f.app.UpdateChatMessage, f.org.ID, f.member.ID, map[string]any{"body": ""}, map[string]string{"id": plain.ID.String()}, nil)
		assert.Equal(t, 400, code)
		_, got := f.fetch(t, f.member, m.ID)
		assert.Len(t, got.Attachments, 1)
	})

	t.Run("archived channels refuse uploads", func(t *testing.T) {
		ch := f.freeChannel(t, "old", f.owner, f.member)
		f.archiveNow(t, ch)
		code, _, body := f.upload(t, f.member, ch, "a.png", pngBytes(2, 2))
		assert.Equal(t, 409, code)
		assert.Contains(t, body, "archived")
	})

	t.Run("who may upload", func(t *testing.T) {
		code, _, _ := f.upload(t, f.readOnl, f.ch, "a.png", pngBytes(2, 2))
		assert.Equal(t, 403, code, "read-only")
		code, _, _ = f.upload(t, f.spare, f.ch, "a.png", pngBytes(2, 2))
		assert.Equal(t, 404, code, "not in the channel")
		code, _, _ = f.upload(t, f.foreign, f.ch, "a.png", pngBytes(2, 2))
		assert.Equal(t, 404, code, "another organization")
	})
	_ = dir
}

func TestAttachments_PendingCapAndDiscard(t *testing.T) {
	f := newPhaseA(t)
	dir := f.withStorage(t)
	var ids []uuid.UUID
	for i := 0; i < 10; i++ {
		ids = append(ids, mustUpload(t, f, f.member, f.ch, "a.png", pngBytes(2, 2)).ID)
	}
	code, _, body := f.upload(t, f.member, f.ch, "a.png", pngBytes(2, 2))
	assert.Equal(t, 400, code)
	assert.Contains(t, body, "too_many_attachments")
	assert.Equal(t, 10, countFiles(t, dir), "the refused upload left no file behind")

	discard := func(u *models.User, id uuid.UUID) int {
		code, _ := chatDo(t, f.app.DeleteChatAttachment, u.OrganizationID, u.ID, nil,
			map[string]string{"id": f.ch.ID.String(), "aid": id.String()}, nil)
		return code
	}
	assert.Equal(t, 404, discard(f.owner, ids[0]), "someone else's pending")
	assert.Equal(t, 200, discard(f.member, ids[0]))
	assert.Equal(t, 9, countFiles(t, dir), "the file went with it")
	assert.Equal(t, 404, discard(f.member, ids[0]), "twice")
	f.sendFiles(t, f.member, f.ch, "", ids[1])
	assert.Equal(t, 404, discard(f.member, ids[1]), "an attached file is no longer discardable")
}

func TestAttachments_StorageFailureLeavesNothing(t *testing.T) {
	f := newPhaseA(t)
	dir := f.withStorage(t)
	rowsBefore := f.rows(t)
	blocker := filepath.Join(dir, "team-chat")
	require.NoError(t, os.WriteFile(blocker, []byte("a file where a directory must be"), 0o644))
	code, _, body := f.upload(t, f.member, f.ch, "a.png", pngBytes(2, 2))
	assert.Equal(t, 503, code)
	assert.Contains(t, body, "storage_unavailable")
	assert.Equal(t, rowsBefore, f.rows(t))
}

func TestAttachments_UploadRateLimit(t *testing.T) {
	f := newPhaseA(t)
	f.withStorage(t)
	defer handlers.SetChatUploadsPerMinuteForTest(3)()
	for i := 0; i < 3; i++ {
		a := mustUpload(t, f, f.member, f.ch, "a.png", pngBytes(2, 2))
		f.sendFiles(t, f.member, f.ch, "", a.ID)
	}
	code, _, body := f.upload(t, f.member, f.ch, "a.png", pngBytes(2, 2))
	assert.Equal(t, 429, code)
	assert.Contains(t, body, "rate_limited")
	code, _, _ = f.upload(t, f.owner, f.ch, "a.png", pngBytes(2, 2))
	assert.Equal(t, 200, code, "the limit is per person")
}

func TestAttachments_Cleanup(t *testing.T) {
	f := newPhaseA(t)
	dir := f.withStorage(t)
	// The test database outlives runs: rows left by earlier ones are old enough to be swept.
	// Run the job once first, so the counts below are only about this test.
	f.app.CleanupChatAttachments(time.Now())
	now := time.Now()
	age := func(id uuid.UUID, d time.Duration) {
		require.NoError(t, f.app.DB.Model(&models.ChatAttachment{}).Where("id = ?", id).Update("created_at", now.Add(-d)).Error)
	}
	stale := mustUpload(t, f, f.member, f.ch, "a.png", pngBytes(2, 2))
	fresh := mustUpload(t, f, f.member, f.ch, "b.png", pngBytes(2, 2))
	age(stale.ID, 2*time.Hour)
	age(fresh.ID, 10*time.Minute)

	keep := mustUpload(t, f, f.member, f.ch, "c.png", pngBytes(2, 2))
	_, mKeep, _ := f.sendFiles(t, f.member, f.ch, "", keep.ID)
	old := mustUpload(t, f, f.member, f.ch, "d.png", pngBytes(2, 2))
	_, mOld, _ := f.sendFiles(t, f.member, f.ch, "", old.ID)
	recent := mustUpload(t, f, f.member, f.ch, "e.png", pngBytes(2, 2))
	_, mRecent, _ := f.sendFiles(t, f.member, f.ch, "", recent.ID)
	age(keep.ID, 90*24*time.Hour) // old but attached to a live message: stays
	require.NoError(t, f.app.DB.Delete(&models.ChatMessage{}, "id = ?", mOld.ID).Error)
	require.NoError(t, f.app.DB.Model(&models.ChatMessage{}).Unscoped().Where("id = ?", mOld.ID).Update("deleted_at", now.Add(-8*24*time.Hour)).Error)
	require.NoError(t, f.app.DB.Delete(&models.ChatMessage{}, "id = ?", mRecent.ID).Error)

	// An orphan file (crash between the file and the row), one old, one new.
	orphanDir := filepath.Join(dir, "team-chat", f.org.ID.String(), f.ch.ID.String())
	oldOrphan := filepath.Join(orphanDir, uuid.New().String()+".png")
	newOrphan := filepath.Join(orphanDir, uuid.New().String()+".png")
	tmpLeft := filepath.Join(orphanDir, ".upload-123")
	for _, p := range []string{oldOrphan, newOrphan, tmpLeft} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}
	require.NoError(t, os.Chtimes(oldOrphan, now.Add(-48*time.Hour), now.Add(-48*time.Hour)))
	require.NoError(t, os.Chtimes(tmpLeft, now.Add(-48*time.Hour), now.Add(-48*time.Hour)))

	res := f.app.CleanupChatAttachments(now)
	assert.Equal(t, 1, res.Pending)
	assert.Equal(t, 1, res.Deleted)
	assert.Equal(t, 2, res.Orphans)

	exists := func(id uuid.UUID) bool {
		var row models.ChatAttachment
		if f.app.DB.First(&row, "id = ?", id).Error != nil {
			return false
		}
		_, err := os.Stat(filepath.Join(dir, row.StoragePath))
		return err == nil
	}
	assert.False(t, exists(stale.ID), "stale pending removed")
	var n int64
	f.app.DB.Model(&models.ChatAttachment{}).Where("id = ?", stale.ID).Count(&n)
	assert.Zero(t, n, "row too")
	assert.True(t, exists(fresh.ID), "fresh pending kept")
	assert.True(t, exists(keep.ID), "attached to a live message: kept however old")
	assert.False(t, exists(old.ID), "file of a message deleted 8 days ago removed")
	var oldRow models.ChatAttachment
	require.NoError(t, f.app.DB.First(&oldRow, "id = ?", old.ID).Error)
	assert.NotNil(t, oldRow.PurgedAt, "and marked purged")
	assert.True(t, exists(recent.ID), "deleted yesterday: still within the grace")
	assert.NoFileExists(t, oldOrphan)
	assert.NoFileExists(t, tmpLeft)
	assert.FileExists(t, newOrphan, "a recent file without a row may be an upload in progress")
	_ = mKeep

	// And a second pass has nothing left to do.
	assert.Equal(t, handlers.ChatCleanupResult{}, f.app.CleanupChatAttachments(now))
}

// The cleanup discards a stale pending file at the very moment a send attaches it:
// exactly one of them wins, and the outcome is consistent either way.
func TestAttachments_Concurrency(t *testing.T) {
	f := newPhaseA(t)
	defer handlers.SetChatUploadsPerMinuteForTest(1000)()
	dir := f.withStorage(t)

	t.Run("the same attachment in two sends: one wins", func(t *testing.T) {
		for round := 0; round < 6; round++ {
			id := mustUpload(t, f, f.member, f.ch, "a.png", pngBytes(2, 2)).ID
			var ok, gone, other int64
			fns := []func(){}
			for i := 0; i < 8; i++ {
				fns = append(fns, func() {
					code, raw := chatDo2(f, f.member, f.ch, id)
					switch {
					case code == 200:
						atomic.AddInt64(&ok, 1)
					case code == 400 && strings.Contains(raw, "attachment_gone"):
						atomic.AddInt64(&gone, 1)
					default:
						atomic.AddInt64(&other, 1)
					}
				})
			}
			runTogether(fns...)
			assert.EqualValues(t, 1, ok, "round %d", round)
			assert.EqualValues(t, 7, gone, "round %d", round)
			assert.Zero(t, other)
			var withIt int64
			f.app.DB.Model(&models.ChatAttachment{}).Where("id = ? AND message_id IS NOT NULL", id).Count(&withIt)
			assert.EqualValues(t, 1, withIt)
		}
	})

	t.Run("a send against the cleanup of the same stale upload", func(t *testing.T) {
		for round := 0; round < 8; round++ {
			a := mustUpload(t, f, f.member, f.ch, "a.png", pngBytes(2, 2))
			require.NoError(t, f.app.DB.Model(&models.ChatAttachment{}).Where("id = ?", a.ID).
				Update("created_at", time.Now().Add(-3*time.Hour)).Error)
			var sendCode int
			runTogether(
				func() { sendCode, _ = chatDo2(f, f.member, f.ch, a.ID) },
				func() { f.app.CleanupChatAttachments(time.Now()) },
			)
			var row models.ChatAttachment
			found := f.app.DB.First(&row, "id = ?", a.ID).Error == nil
			if sendCode == 200 {
				require.True(t, found, "round %d: sent, so the row stays", round)
				require.NotNil(t, row.MessageID)
				assert.FileExists(t, filepath.Join(dir, row.StoragePath), "round %d: and so does its file", round)
			} else {
				assert.Equal(t, 400, sendCode, "round %d", round)
				assert.False(t, found, "round %d: refused, so the cleanup removed it", round)
			}
		}
	})

	t.Run("archiving while uploading and sending: never after the archive", func(t *testing.T) {
		for round := 0; round < 4; round++ {
			f.app.DB.Where("uploader_id = ? AND message_id IS NULL", f.member.ID).Delete(&models.ChatAttachment{}) // refused sends leave pending uploads
			ch := f.freeChannel(t, "busy", f.owner, f.member)
			ids := make([]uuid.UUID, 6)
			for i := range ids {
				ids[i] = mustUpload(t, f, f.member, ch, "a.png", pngBytes(2, 2)).ID
			}
			var other int64
			fns := []func(){func() {
				if callSafe(f.app.ArchiveChatChannel, f.org.ID, f.owner.ID, nil, f.path(ch)) != 200 {
					atomic.AddInt64(&other, 1)
				}
			}}
			for _, id := range ids {
				fns = append(fns, func() {
					code, _ := chatDo2(f, f.member, ch, id)
					if code != 200 && code != 409 {
						atomic.AddInt64(&other, 1)
					}
				})
			}
			runTogether(fns...)
			assert.Zero(t, other, "round %d: only 200 or 409; a deadlock would be 500", round)
			fresh := f.reload(t, ch)
			require.NotNil(t, fresh.ArchivedAt)
			var msgs []models.ChatMessage
			f.app.DB.Where("channel_id = ? AND kind = ?", ch.ID, models.ChatMessageKindUser).Find(&msgs)
			for _, m := range msgs {
				assert.False(t, m.CreatedAt.After(*fresh.ArchivedAt), "round %d", round)
			}
		}
	})

	t.Run("deleting the message while it is being downloaded", func(t *testing.T) {
		f.app.DB.Where("uploader_id = ? AND message_id IS NULL", f.member.ID).Delete(&models.ChatAttachment{})
		a := mustUpload(t, f, f.member, f.ch, "a.png", pngBytes(30, 30))
		_, m, _ := f.sendFiles(t, f.member, f.ch, "", a.ID)
		var bad int64
		fns := []func(){func() { callSafe(f.app.DeleteChatMessage, f.org.ID, f.member.ID, nil, map[string]string{"id": m.ID.String()}) }}
		for i := 0; i < 10; i++ {
			fns = append(fns, func() {
				code := downloadSafe(f, f.owner, a.ID)
				if code != 200 && code != 404 {
					atomic.AddInt64(&bad, 1)
				}
			})
		}
		runTogether(fns...)
		assert.Zero(t, bad)
		assert.Equal(t, 404, testutil.GetResponseStatusCode(f.download(t, f.owner, a.ID, "")))
	})
}

// chatDo2 sends a message with one attachment from a goroutine (never calls t.FailNow).
func chatDo2(f *phaseA, u *models.User, ch *models.ChatChannel, id uuid.UUID) (int, string) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetContentType("application/json")
	ctx.Request.Header.SetMethod("POST")
	b, _ := json.Marshal(map[string]any{"body": "", "attachment_ids": []string{id.String()}})
	ctx.Request.SetBody(b)
	req := &fastglue.Request{RequestCtx: ctx}
	testutil.SetAuthContext(req, u.OrganizationID, u.ID)
	testutil.SetPathParam(req, "id", ch.ID.String())
	if err := f.app.SendChatMessage(req); err != nil {
		return -1, err.Error()
	}
	return ctx.Response.StatusCode(), string(ctx.Response.Body())
}

// --- Regression: replies and reactions keep working with files ---

func TestAttachments_RepliesAndReactions(t *testing.T) {
	f := newPhaseA(t)
	defer handlers.SetChatUploadsPerMinuteForTest(1000)()
	f.withStorage(t)
	img := mustUpload(t, f, f.member, f.ch, "foto.png", pngBytes(2, 2))
	_, filesOnly, _ := f.sendFiles(t, f.member, f.ch, "", img.ID)
	voice := mustUpload(t, f, f.owner, f.ch, "a.webm", webmAudio(), "kind", "audio", "duration_ms", "4000")
	_, voiceMsg, _ := f.sendFiles(t, f.owner, f.ch, "", voice.ID)

	t.Run("replying to a message that is only a file", func(t *testing.T) {
		code, r, _ := f.reply(t, f.ch, f.owner, "bonita", filesOnly.ID)
		require.Equal(t, 200, code)
		require.NotNil(t, r.ReplyTo)
		assert.Empty(t, r.ReplyTo.Snippet)
		assert.Equal(t, "image", r.ReplyTo.AttachmentKind)
		assert.Equal(t, "foto.png", r.ReplyTo.AttachmentName)
		code, r2, _ := f.reply(t, f.ch, f.member, "ouvi", voiceMsg.ID)
		require.Equal(t, 200, code)
		assert.Equal(t, "audio", r2.ReplyTo.AttachmentKind)
	})

	t.Run("a reply may carry files of its own", func(t *testing.T) {
		a := mustUpload(t, f, f.member, f.ch, "c.pdf", pdfBytes())
		code, raw := chatDo(t, f.app.SendChatMessage, f.org.ID, f.member.ID,
			map[string]any{"body": "segue", "parent_id": filesOnly.ID.String(), "attachment_ids": []string{a.ID.String()}},
			map[string]string{"id": f.ch.ID.String()}, nil)
		require.Equal(t, 200, code, string(raw))
		m := decodeChat[handlers.ChatMessageResponse](t, raw)
		assert.Len(t, m.Attachments, 1)
		assert.NotNil(t, m.ReplyTo)
	})

	t.Run("reactions on a message with files", func(t *testing.T) {
		require.Equal(t, 200, okStatus(f.react(t, true, f.owner, filesOnly.ID, "heart")))
		_, got := f.fetch(t, f.member, filesOnly.ID)
		assert.Len(t, got.Reactions, 1)
		assert.Len(t, got.Attachments, 1)
	})

	t.Run("deleting hides files and reactions together, and quotes turn into 'removed'", func(t *testing.T) {
		code, _ := chatDo(t, f.app.DeleteChatMessage, f.org.ID, f.member.ID, nil, map[string]string{"id": filesOnly.ID.String()}, nil)
		require.Equal(t, 200, code)
		for _, m := range f.page(t, f.owner, f.ch) {
			assert.NotEqual(t, filesOnly.ID, m.ID)
			if m.ReplyTo != nil && m.ReplyTo.ID == filesOnly.ID {
				assert.True(t, m.ReplyTo.Deleted)
				assert.Empty(t, m.ReplyTo.AttachmentName, "the name of a deleted message's file is not leaked")
			}
		}
	})
}

func TestAttachments_ListUsesConstantQueries(t *testing.T) {
	f := newPhaseA(t)
	f.withStorage(t)
	var queries int64
	require.NoError(t, f.app.DB.Callback().Query().After("gorm:query").Register("count_chat_attachment_queries", func(*gorm.DB) {
		atomic.AddInt64(&queries, 1)
	}))
	seed := func(ch *models.ChatChannel, n int) {
		prev := f.msg(t, ch, f.owner, "root")
		for i := 0; i < n; i++ {
			a := mustUpload(t, f, f.member, ch, "a.png", pngBytes(2, 2))
			code, raw := chatDo(t, f.app.SendChatMessage, f.org.ID, f.member.ID,
				map[string]any{"body": "m", "parent_id": prev.ID.String(), "attachment_ids": []string{a.ID.String()}},
				map[string]string{"id": ch.ID.String()}, nil)
			require.Equal(t, 200, code, string(raw))
			prev = decodeChat[handlers.ChatMessageResponse](t, raw)
			require.Equal(t, 200, okStatus(f.react(t, true, f.owner, prev.ID, "heart")))
		}
	}
	measure := func(ch *models.ChatChannel) int64 {
		atomic.StoreInt64(&queries, 0)
		f.page(t, f.member, ch)
		return atomic.LoadInt64(&queries)
	}
	defer handlers.SetChatUploadsPerMinuteForTest(1000)()
	small := f.freeChannel(t, "small", f.owner, f.member)
	big := f.freeChannel(t, "big", f.owner, f.member)
	seed(small, 2)
	seed(big, 9)
	assert.Equal(t, measure(small), measure(big), "files, quotes and reactions do not add queries per message")
}

// downloadSafe fetches an attachment from a goroutine and drains the body (which
// closes the file, as the server does after writing the response).
func downloadSafe(f *phaseA, u *models.User, id uuid.UUID) int {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	req := &fastglue.Request{RequestCtx: ctx}
	testutil.SetAuthContext(req, u.OrganizationID, u.ID)
	testutil.SetPathParam(req, "id", id.String())
	if err := f.app.DownloadChatAttachment(req); err != nil {
		return -1
	}
	_ = ctx.Response.Body()
	return ctx.Response.StatusCode()
}

// uploadSafe is upload for goroutines: it never calls t.FailNow and returns the status.
func (f *phaseA) uploadSafe(u *models.User, ch *models.ChatChannel, name string, data []byte) int {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, name))
	h.Set("Content-Type", "application/octet-stream")
	part, err := mw.CreatePart(h)
	if err != nil {
		return -1
	}
	_, _ = part.Write(data)
	_ = mw.Close()
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.SetContentType(mw.FormDataContentType())
	ctx.Request.SetBody(buf.Bytes())
	req := &fastglue.Request{RequestCtx: ctx}
	testutil.SetAuthContext(req, u.OrganizationID, u.ID)
	testutil.SetPathParam(req, "id", ch.ID.String())
	if err := f.app.UploadChatAttachment(req); err != nil {
		return -1
	}
	return ctx.Response.StatusCode()
}

// The cap of 10 pending uploads is an invariant, not a hint: parallel requests that
// all read the same count must not get past it.
func TestAttachments_PendingCapHoldsUnderConcurrency(t *testing.T) {
	f := newPhaseA(t)
	f.withStorage(t)
	defer handlers.SetChatUploadsPerMinuteForTest(1000)()
	for round := 0; round < 3; round++ {
		f.app.DB.Where("uploader_id = ? AND message_id IS NULL", f.member.ID).Delete(&models.ChatAttachment{})
		var accepted, refused, other int64
		fns := []func(){}
		for i := 0; i < 30; i++ {
			fns = append(fns, func() {
				switch f.uploadSafe(f.member, f.ch, "a.png", pngBytes(2, 2)) {
				case 200:
					atomic.AddInt64(&accepted, 1)
				case 400:
					atomic.AddInt64(&refused, 1)
				default:
					atomic.AddInt64(&other, 1)
				}
			})
		}
		runTogether(fns...)
		var pending int64
		f.app.DB.Model(&models.ChatAttachment{}).Where("uploader_id = ? AND message_id IS NULL", f.member.ID).Count(&pending)
		assert.EqualValues(t, 10, pending, "round %d: never more than the cap", round)
		assert.EqualValues(t, 10, accepted, "round %d", round)
		assert.EqualValues(t, 20, refused, "round %d", round)
		assert.Zero(t, other, "round %d: no 500 (a deadlock would be one)", round)
	}
}

// Same for the 20-different-emojis cap on one message: with 19 kinds in place, twelve
// people each adding a different emoji at once get exactly one more in.
func TestReactions_KindCapHoldsUnderConcurrency(t *testing.T) {
	f := newPhaseA(t)
	codes := []string{"thumbs_up", "heart", "laugh", "wow", "sad", "pray", "check", "eyes", "fire", "party", "thinking", "clap"}
	var users []*models.User
	for range codes {
		u := chatUser(t, f.app, f.org.ID, teamChatKeysOf("agent"))
		require.NoError(t, f.app.DB.Create(&models.ChatChannelMember{OrganizationID: f.org.ID, ChannelID: f.ch.ID, UserID: u.ID, Role: "member"}).Error)
		users = append(users, u)
	}
	// The window is narrow, so many rounds, each on a fresh message.
	for round := 0; round < 25; round++ {
		m := f.msg(t, f.ch, f.owner, fmt.Sprintf("popular %d", round))
		rows := make([]models.ChatMessageReaction, 19)
		for i := range rows {
			rows[i] = models.ChatMessageReaction{ID: uuid.New(), OrganizationID: f.org.ID, ChannelID: f.ch.ID, MessageID: m.ID,
				UserID: f.owner.ID, Code: fmt.Sprintf("seed_%02d", i)}
		}
		require.NoError(t, f.app.DB.Create(&rows).Error)
		var ok, full, other int64
		fns := []func(){}
		for i, code := range codes {
			fns = append(fns, func() {
				switch callSafe(f.app.PutChatReaction, f.org.ID, users[i].ID, nil, map[string]string{"id": m.ID.String(), "code": code}) {
				case 200:
					atomic.AddInt64(&ok, 1)
				case 409:
					atomic.AddInt64(&full, 1)
				default:
					atomic.AddInt64(&other, 1)
				}
			})
		}
		runTogether(fns...)
		var kinds int64
		f.app.DB.Model(&models.ChatMessageReaction{}).Where("message_id = ?", m.ID).Distinct("code").Count(&kinds)
		require.EqualValues(t, 20, kinds, "round %d: never past the cap", round)
		require.EqualValues(t, 1, ok, "round %d", round)
		require.EqualValues(t, 11, full, "round %d", round)
		require.Zero(t, other, "round %d", round)
	}
}

// A voice-memo format (m4a) sent as a plain file is refused: the client must not offer it.
func TestAttachments_AudioContainersNeedTheRecordingClaim(t *testing.T) {
	f := newPhaseA(t)
	f.withStorage(t)
	for _, c := range []struct {
		name string
		data []byte
	}{{"nota.m4a", m4aAudio()}, {"nota.webm", webmAudio()}} {
		code, _, body := f.upload(t, f.member, f.ch, c.name, c.data)
		assert.Equal(t, 415, code, c.name)
		assert.Contains(t, body, "file_type_not_allowed")
		code, _, _ = f.upload(t, f.member, f.ch, c.name, c.data, "kind", "audio")
		assert.Equal(t, 200, code, c.name+" as a recording")
		f.app.DB.Where("uploader_id = ? AND message_id IS NULL", f.member.ID).Delete(&models.ChatAttachment{})
	}
}

// attachment_gone names the attachments that are gone, so the client drops exactly those.
func TestAttachments_GoneNamesTheMissingIds(t *testing.T) {
	f := newPhaseA(t)
	f.withStorage(t)
	good1 := mustUpload(t, f, f.member, f.ch, "a.png", pngBytes(2, 2)).ID
	good2 := mustUpload(t, f, f.member, f.ch, "b.png", pngBytes(2, 2)).ID
	stale := mustUpload(t, f, f.member, f.ch, "c.png", pngBytes(2, 2)).ID
	foreign := mustUpload(t, f, f.owner, f.ch, "d.png", pngBytes(2, 2)).ID
	require.NoError(t, f.app.DB.Where("id = ?", stale).Delete(&models.ChatAttachment{}).Error) // as the cleanup does

	code, body := chatDo(t, f.app.SendChatMessage, f.org.ID, f.member.ID,
		map[string]any{"body": "x", "attachment_ids": []string{good1.String(), stale.String(), good2.String(), foreign.String()}},
		map[string]string{"id": f.ch.ID.String()}, nil)
	assert.Equal(t, 400, code)
	var resp struct {
		ErrorType string `json:"error_type"`
		Data      struct {
			IDs []uuid.UUID `json:"attachment_ids"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, "attachment_gone", resp.ErrorType)
	assert.ElementsMatch(t, []uuid.UUID{stale, foreign}, resp.Data.IDs, "exactly the unusable ones")

	code2, _, _ := f.sendFiles(t, f.member, f.ch, "ok", good1, good2)
	assert.Equal(t, 200, code2, "the good ones were not consumed by the failed send")
}

// The upload answer says how long the pending file is guaranteed to live, so the
// client does not keep its own copy of the server's grace period.
func TestAttachments_UploadReportsPendingTTL(t *testing.T) {
	f := newPhaseA(t)
	f.withStorage(t)
	a := mustUpload(t, f, f.member, f.ch, "a.png", pngBytes(2, 2))
	assert.EqualValues(t, time.Hour.Milliseconds(), a.PendingTTLMs)
	_, msg, _ := f.sendFiles(t, f.member, f.ch, "", a.ID)
	require.Len(t, msg.Attachments, 1)
	assert.Zero(t, msg.Attachments[0].PendingTTLMs, "only the upload answer carries it")
}

// A duplicated id is refused, and nothing is claimed as missing (the client then drops none).
func TestAttachments_DuplicateIdIsRefusedWithoutNamingAnyone(t *testing.T) {
	f := newPhaseA(t)
	f.withStorage(t)
	id := mustUpload(t, f, f.member, f.ch, "a.png", pngBytes(2, 2)).ID
	code, body := chatDo(t, f.app.SendChatMessage, f.org.ID, f.member.ID,
		map[string]any{"body": "x", "attachment_ids": []string{id.String(), id.String()}},
		map[string]string{"id": f.ch.ID.String()}, nil)
	assert.Equal(t, 400, code)
	var resp struct {
		ErrorType string `json:"error_type"`
		Data      struct {
			IDs []uuid.UUID `json:"attachment_ids"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, "attachment_gone", resp.ErrorType)
	assert.Empty(t, resp.Data.IDs)
	code, _, _ = f.sendFiles(t, f.member, f.ch, "ok", id)
	assert.Equal(t, 200, code, "the refused send consumed nothing")
}
