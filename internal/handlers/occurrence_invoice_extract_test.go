package handlers

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keyWithDV builds a valid 44-digit access key for number n, series s.
func keyWithDV(uf, aamm, cnpj string, series, n int) string {
	body := uf + aamm + cnpj + "55" + pad(series, 3) + pad(n, 9) + "1" + "12345678"
	sum, w := 0, 2
	for i := len(body) - 1; i >= 0; i-- {
		sum += int(body[i]-'0') * w
		if w++; w > 9 {
			w = 2
		}
	}
	dv := 11 - sum%11
	if dv >= 10 {
		dv = 0
	}
	return body + strconv.Itoa(dv)
}

func pad(n, width int) string {
	s := strconv.Itoa(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

func TestAccessKey_CheckDigitAndFields(t *testing.T) {
	k := keyWithDV("35", "2610", "12345678000195", 1, 4321)
	require.Len(t, k, 44)
	assert.True(t, accessKeyValid(k))
	series, number := fromAccessKey(k)
	assert.Equal(t, "1", series)
	assert.Equal(t, "4321", number)
	bad := k[:43] + string(rune('0'+(int(k[43]-'0')+1)%10))
	assert.False(t, accessKeyValid(bad), "a wrong check digit is not trusted")
	assert.False(t, accessKeyValid("123"))
}

func TestParseNFeXML_ReadsFieldsAndItems(t *testing.T) {
	k := keyWithDV("35", "2610", "12345678000195", 2, 987)
	xml := `<?xml version="1.0" encoding="UTF-8"?><nfeProc xmlns="http://www.portalfiscal.inf.br/nfe"><NFe><infNFe Id="NFe` + k + `" versao="4.00">
<ide><serie>2</serie><nNF>000987</nNF><dhEmi>2026-10-20T10:15:00-03:00</dhEmi></ide>
<emit><xNome>Fábrica de Pisos Ltda</xNome></emit>
<det nItem="1"><prod><cProd>PS-100</cProd><xProd>Porcelanato 60x60</xProd><uCom>M2</uCom><qCom>12.5000</qCom></prod></det>
<det nItem="2"><prod><cProd>RJ-1</cProd><xProd>Rejunte cinza</xProd><uCom>CX</uCom><qCom>3.0000</qCom></prod></det>
</infNFe></NFe></nfeProc>`
	res, ok := parseNFeXML([]byte(xml))
	require.True(t, ok)
	assert.Equal(t, "xml", res.Method)
	assert.Equal(t, "987", res.Number)
	assert.Equal(t, "2", res.Series)
	assert.Equal(t, "2026-10-20", res.IssueDate)
	assert.Equal(t, k, res.AccessKey)
	assert.Equal(t, "Fábrica de Pisos Ltda", res.Issuer)
	require.Len(t, res.Items, 2)
	assert.Equal(t, invoiceItem{Code: "PS-100", Description: "Porcelanato 60x60", Quantity: "12.5 M2"}, res.Items[0])
	assert.Empty(t, res.Uncertain)

	// A wrong check digit is flagged, not hidden.
	bad := k[:43] + string(rune('0'+(int(k[43]-'0')+1)%10))
	res, ok = parseNFeXML([]byte(`<NFe><infNFe Id="NFe` + bad + `"><ide><nNF>5</nNF></ide></infNFe></NFe>`))
	require.True(t, ok)
	assert.Contains(t, res.Uncertain, "access_key")
}

func TestParseNFeXML_RefusesWhatIsNotAnInvoiceOrIsHostile(t *testing.T) {
	for name, doc := range map[string]string{
		"not an nfe": `<?xml version="1.0"?><pedido><n>1</n></pedido>`,
		"entities":   `<?xml version="1.0"?><!DOCTYPE x [<!ENTITY a SYSTEM "file:///etc/passwd">]><NFe><infNFe><ide><nNF>&a;</nNF></ide></infNFe></NFe>`,
		"no number":  `<NFe><infNFe><ide></ide></infNFe></NFe>`,
		"malformed":  `<NFe><infNFe>`,
	} {
		_, ok := parseNFeXML([]byte(doc))
		assert.False(t, ok, name)
	}
}

func TestParseDanfeText_UsesTheAccessKeyAndSaysWhatItDidNotRead(t *testing.T) {
	k := keyWithDV("35", "2610", "12345678000195", 3, 55)
	spaced := ""
	for i := 0; i < 44; i += 4 {
		spaced += k[i:i+4] + " "
	}
	res := parseDanfeText("DANFE\nCHAVE DE ACESSO\n" + spaced + "\nDATA DE EMISSÃO 21/10/2026\n")
	assert.Equal(t, "pdf_text", res.Method)
	assert.Equal(t, "55", res.Number)
	assert.Equal(t, "3", res.Series)
	assert.Equal(t, k, res.AccessKey)
	assert.Equal(t, "2026-10-21", res.IssueDate)
	assert.Empty(t, res.Uncertain)
	assert.Contains(t, res.Warnings, "items_not_read")

	// No valid key: nothing invented.
	none := parseDanfeText("Nota sem chave. Emitida em 03/04/2026.")
	assert.Empty(t, none.Number)
	assert.Contains(t, none.Warnings, "no_access_key")
	assert.Contains(t, none.Uncertain, "issue_date", "the first date on the page is only a guess")
}
