package handlers

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The generated report must be a PDF a real reader opens, with accents and across several pages.
func TestPDFWriter_ProducesAReadableDocument(t *testing.T) {
	bin, err := exec.LookPath("pdftotext")
	if err != nil {
		t.Skip("pdftotext not installed")
	}
	d := newPDF("Protocolo 2026-000222 - Pedido (55501)", "Gerado em 26/10/2026")
	d.put("Histórico — ação (teste) e outro", 12, true, 0)
	for i := 0; i < 120; i++ {
		d.paragraph(strings.Repeat("palavra longa de teste com acentuação ", 4), 10)
	}
	data := d.bytes()
	require.Greater(t, len(d.pages), 1, "page breaks")

	f, err := os.CreateTemp(t.TempDir(), "r-*.pdf")
	require.NoError(t, err)
	_, _ = f.Write(data)
	_ = f.Close()
	out, err := exec.Command(bin, "-q", "-enc", "UTF-8", f.Name(), "-").Output()
	require.NoError(t, err, "pdftotext must open the file")
	text := string(out)
	assert.Contains(t, text, "Histórico")
	assert.Contains(t, text, "ação")
	assert.Contains(t, text, "(teste)")
	assert.Contains(t, text, "acentuação")
}
