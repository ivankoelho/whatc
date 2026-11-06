package handlers_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func (w *chatWorld) export(t *testing.T, u *models.User, occID uuid.UUID, sections []string, format string) (int, []byte, *fastglue.Request) {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{"sections": sections, "format": format})
	testutil.SetAuthContext(req, u.OrganizationID, u.ID)
	testutil.SetPathParam(req, "id", occID.String())
	require.NoError(t, w.app.ExportOccurrence(req))
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req), req
}

func unzip(t *testing.T, data []byte) map[string][]byte {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		out[f.Name] = b
	}
	return out
}

func TestOccurrenceExport_FollowsWhatEachPersonMayRead(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	require.Equal(t, fasthttp.StatusOK, w.say(t, w.sac.ID, "SAC: cliente aguardando previsão"))
	w.addEvent(t, models.OccurrenceEventNote, "=HYPERLINK(\"http://x\")", w.logPorto.ID, time.Now())
	w.addEvent(t, models.OccurrenceEventReply, "SEGREDO combinado com o cliente", w.logPorto.ID, time.Now().Add(-time.Minute))

	// Options mirror the permissions.
	opts := func(u *models.User) map[string]bool {
		_, raw, _ := w.fileCall(t, w.app.GetOccurrenceExportOptions, u, w.occID, "", "")
		var env struct {
			Data struct {
				Sections map[string]bool `json:"sections"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(raw, &env))
		return env.Data.Sections
	}
	assert.True(t, opts(w.logPorto)["whatsapp"])
	assert.False(t, opts(w.sac)["whatsapp"], "following the case is not reading the conversation")
	assert.True(t, opts(w.sac)["history"])

	// A section one may not have is refused, never silently dropped.
	code, _, _ := w.export(t, w.sac, w.occID, []string{"summary", "whatsapp"}, "pdf")
	assert.Equal(t, fasthttp.StatusForbidden, code)

	// "full" gives what is allowed and says what was left out.
	code, pdf, req := w.export(t, w.sac, w.occID, []string{"full"}, "pdf")
	require.Equal(t, fasthttp.StatusOK, code)
	assert.True(t, bytes.HasPrefix(pdf, []byte("%PDF-1.4")))
	assert.Contains(t, string(pdf), latin1("CÓDIGO DO PROTOCOLO"), "the report names the protocol")
	assert.Contains(t, string(pdf), latin1("SEÇÕES NÃO INCLUÍDAS"), "the omitted sections are listed")
	assert.NotContains(t, string(pdf), "SEGREDO", "the customer message text is withheld from someone who cannot read that conversation")
	assert.Equal(t, "application/pdf", string(req.RequestCtx.Response.Header.Peek("Content-Type")))

	// Who works the case gets the conversation and the withheld text too.
	code, pdf, _ = w.export(t, w.logPorto, w.occID, []string{"summary", "history", "whatsapp", "internal_chat"}, "pdf")
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Contains(t, string(pdf), "SEGREDO")
	assert.Contains(t, string(pdf), "cliente aguardando")

	// CSV: one table; a spreadsheet formula is neutralized; BOM for Excel.
	code, csvb, _ := w.export(t, w.logPorto, w.occID, []string{"history"}, "csv")
	require.Equal(t, fasthttp.StatusOK, code)
	assert.True(t, bytes.HasPrefix(csvb, []byte("\xEF\xBB\xBF")))
	assert.Contains(t, string(csvb), "'=HYPERLINK")
	assert.NotContains(t, string(csvb), ";=HYPERLINK")
	code, _, _ = w.export(t, w.logPorto, w.occID, []string{"history", "whatsapp"}, "csv")
	assert.Equal(t, fasthttp.StatusBadRequest, code, "CSV holds one table")

	// Bad requests.
	for _, c := range []struct {
		s []string
		f string
	}{{nil, "pdf"}, {[]string{"nope"}, "pdf"}, {[]string{"summary"}, "docx"}} {
		code, _, _ = w.export(t, w.logPorto, w.occID, c.s, c.f)
		assert.Equal(t, fasthttp.StatusBadRequest, code)
	}

	// Scope: another store and another organization get nothing; the id comes only from the path.
	code, _, _ = w.export(t, w.logOutra, w.occID, []string{"summary"}, "pdf")
	assert.Equal(t, fasthttp.StatusForbidden, code)
	other := newIsoWorld(t)
	foreign := chatUser(t, w.app, other.org.ID, caseSupervisorPerms)
	code, _, _ = w.export(t, foreign, w.occID, []string{"summary"}, "pdf")
	assert.NotEqual(t, fasthttp.StatusOK, code)

	// Every export is audited.
	require.Eventually(t, func() bool {
		var n int64
		w.app.DB.Model(&models.AuditLog{}).Where("resource_type = ? AND resource_id = ? AND action = ?", "occurrence_export", w.occID, models.AuditActionAccessed).Count(&n)
		return n >= 3
	}, 5*time.Second, 25*time.Millisecond)
}

func TestOccurrenceExport_ZipCarriesOnlyFilesThePersonMayDownload(t *testing.T) {
	w := newFilesWorld(t)
	w.run()
	_, raw := w.fileUpload(t, w.logPorto, w.occID, "restrito.pdf", pdfBytes())
	require.NotEmpty(t, raw)
	_, raw = w.fileUpload(t, w.logPorto, w.occID, "aberto.pdf", append(pdfBytes(), 'z'), "restricted", "false")
	require.NotEmpty(t, raw)

	names := func(u *models.User) []string {
		code, zb, _ := w.export(t, u, w.occID, []string{"documents"}, "zip")
		require.Equal(t, fasthttp.StatusOK, code)
		var out []string
		for n := range unzip(t, zb) {
			out = append(out, n)
		}
		return out
	}
	sac := names(w.sac)
	assert.Contains(t, sac, "relatorio.pdf")
	assert.Contains(t, sac, "documents.csv")
	joined := ""
	for _, n := range sac {
		joined += n + "|"
	}
	assert.Contains(t, joined, "aberto.pdf")
	assert.NotContains(t, joined, "restrito.pdf", "a restricted file is listed but its bytes are not exported to someone who cannot download it")

	works := ""
	for _, n := range names(w.logPorto) {
		works += n + "|"
	}
	assert.Contains(t, works, "restrito.pdf")
	assert.Contains(t, works, "aberto.pdf")
}

// latin1 turns text into the single-byte form the PDF writer uses (WinAnsi agrees with Latin-1 for these letters).
func latin1(s string) string {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		b = append(b, byte(r))
	}
	return string(b)
}
