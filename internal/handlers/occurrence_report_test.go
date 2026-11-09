package handlers_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func pdfText(t *testing.T, pdf []byte) string {
	t.Helper()
	bin, err := exec.LookPath("pdftotext")
	if err != nil {
		t.Skip("pdftotext not installed")
	}
	f, err := os.CreateTemp(t.TempDir(), "r-*.pdf")
	require.NoError(t, err)
	_, _ = f.Write(pdf)
	_ = f.Close()
	out, err := exec.Command(bin, "-q", "-enc", "UTF-8", "-layout", f.Name(), "-").Output()
	require.NoError(t, err, "the report must open in a PDF reader")
	return string(out)
}

// A report of a busy case: many history entries, long text, several people, files. Everything the system
// recorded is in it, dates read DD/MM/YYYY HH:mm, and the document claims nothing it cannot back up.
func TestReport_LongHistoryManyPartiesAndFiles(t *testing.T) {
	w := newSupportWorld(t)
	org := w.logPorto.OrganizationID

	// people: a support person from Logística, a follower, and a removed support person (history only)
	code, raw := w.add(t, w.logPorto, map[string]any{"user_id": w.apoio.ID.String(), "role": "support", "department_id": w.depLog.ID.String()})
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	code, raw = w.add(t, w.logPorto, map[string]any{"user_id": w.sac.ID.String()})
	require.Equal(t, fasthttp.StatusOK, code, string(raw))

	// a long history with accents, a very long unbroken word, and enough entries to need several pages
	base := time.Date(2026, 10, 13, 16, 43, 58, 0, time.UTC) // 13:43 in the organization zone (-03)
	long := strings.Repeat("Cliente aguarda previsão de entrega do pedido com acentuação e ç. ", 12)
	for i := 0; i < 150; i++ {
		content := fmt.Sprintf("Registro %03d: %s", i, long)
		if i == 7 {
			content = "PALAVRA_MUITO_LONGA_" + strings.Repeat("X", 160)
		}
		ev := models.OccurrenceEvent{OrganizationID: org, OccurrenceID: w.occID, Type: models.OccurrenceEventNote,
			Content: content, CreatedByID: &w.logPorto.ID}
		require.NoError(t, w.app.DB.Create(&ev).Error)
		require.NoError(t, w.app.DB.Model(&ev).Update("created_at", base.Add(time.Duration(i)*time.Minute)).Error)
	}
	// files: one with an accented long name, one without content
	f1 := w.upl(t, w.logPorto, "Contrato_para_Colaboração_mais_Serviços_-_Zoho_2025-12-09_(Atualizado).pdf", pdfBytes())
	require.NotEmpty(t, f1.ID)

	code, pdf, req := w.export(t, w.logPorto, w.occID, []string{"full"}, "pdf")
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, "application/pdf", string(req.RequestCtx.Response.Header.Peek("Content-Type")))
	text := pdfText(t, pdf)

	// identity, protocol and the case data
	assert.Contains(t, text, "RELATÓRIO DE OCORRÊNCIA")
	assert.Contains(t, text, "CÓDIGO DO PROTOCOLO")
	assert.Contains(t, text, "DADOS DA OCORRÊNCIA")
	assert.Contains(t, text, "PRIORIDADE")
	assert.Contains(t, text, "PRAZO PARA RESOLVER")

	// the parties, each with the role and the department it was picked from
	assert.Contains(t, text, "PARTES ENVOLVIDAS")
	assert.Contains(t, text, "Solicitante")
	assert.Contains(t, text, "Pessoa de apoio")
	assert.Contains(t, text, w.apoio.FullName)
	assert.Contains(t, text, w.depLog.Name)
	assert.Contains(t, text, "Participante acompanhando")
	assert.Contains(t, text, w.sac.FullName)

	// the whole history is there, in order, and nothing was cut: first, a late one, the long word split across lines
	assert.Contains(t, text, "HISTÓRICO DE INTERAÇÕES")
	assert.Contains(t, text, "Registro 000")
	assert.Contains(t, text, "Registro 149")
	assert.Less(t, strings.Index(text, "Registro 000"), strings.Index(text, "Registro 100"))
	assert.GreaterOrEqual(t, strings.Count(text, "X"), 160, "every character of an unbreakable word is printed (split over lines, none lost)")
	assert.Contains(t, text, "acentuação")

	// the people added are in the history as events too
	assert.Contains(t, text, "adicionou "+w.apoio.FullName+" como apoio")

	// dates: DD/MM/YYYY and HH:mm in the organization zone (13:43 for the first entry), never ISO or 12 h
	assert.Contains(t, text, "13/10/2026")
	assert.Regexp(t, regexp.MustCompile(`(?s)13/10/2026.{0,200}?13:43`), text)
	assert.NotRegexp(t, regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}`), text)
	assert.NotRegexp(t, regexp.MustCompile(`(?i)\b(AM|PM)\b`), text)

	// files
	assert.Contains(t, text, "DOCUMENTOS ANEXADOS")
	assert.Contains(t, text, "Colaboração")

	// issuance record: who, when (with seconds), code; the code is also written to the audit log
	assert.Contains(t, text, "REGISTRO DE EMISSÃO")
	assert.Contains(t, text, w.logPorto.FullName)
	ref := regexp.MustCompile(`WHC-\d+-\d{14}-[0-9A-F]{4}`).FindString(text)
	require.NotEmpty(t, ref, "reference code")
	assert.Regexp(t, regexp.MustCompile(`\d{2}/\d{2}/\d{4} \d{2}:\d{2}:\d{2}`), text)
	require.Eventually(t, func() bool {
		var rows []models.AuditLog
		w.app.DB.Where("organization_id = ? AND resource_id = ? AND user_id = ?", org, w.occID, w.logPorto.ID).Find(&rows)
		for _, r := range rows {
			b, _ := json.Marshal(r.Changes)
			if strings.Contains(string(b), ref) {
				return true
			}
		}
		return false
	}, 3*time.Second, 100*time.Millisecond, "the reference code is recorded in the audit log")

	// no page header/footer overlap with content, several pages, numbered
	assert.Regexp(t, regexp.MustCompile(`Página 1 de \d+`), text)
	assert.Greater(t, strings.Count(text, "\f"), 3, "a long history takes several pages")

	// what the system does not do is not claimed
	low := strings.ToLower(text)
	for _, claim := range []string{"autenticado", "assinatura digital válida", "imutável", "confirmação de leitura", "lida em", "verificado"} {
		assert.NotContains(t, low, claim, "claim without a mechanism behind it")
	}
	assert.Contains(t, strings.Join(strings.Fields(low), " "), "não possui assinatura digital")
}


// The same report for someone who only follows the case never carries the customer conversation, and the
// same permission rules as before still apply to what is exported.
func TestReport_FollowerCannotGetWhatsAppOrDiscussionInIt(t *testing.T) {
	w := newSupportWorld(t)
	code, raw := w.add(t, w.logPorto, map[string]any{"user_id": w.apoio.ID.String(), "role": "support", "department_id": w.depLog.ID.String()})
	require.Equal(t, fasthttp.StatusOK, code, string(raw))

	code, _, _ = w.export(t, w.apoio, w.occID, []string{"whatsapp"}, "pdf")
	assert.Equal(t, fasthttp.StatusForbidden, code)
	code, pdf, _ := w.export(t, w.apoio, w.occID, []string{"full"}, "pdf")
	require.Equal(t, fasthttp.StatusOK, code)
	text := pdfText(t, pdf)
	assert.NotContains(t, text, "CONVERSA WHATSAPP")
	assert.Contains(t, text, "SEÇÕES NÃO INCLUÍDAS")
}
