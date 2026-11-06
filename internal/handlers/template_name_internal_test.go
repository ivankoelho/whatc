package handlers

import "testing"

func TestNormalizeTemplateName_TransliteratesAccents(t *testing.T) {
	cases := map[string]string{
		"código_acesso":    "codigo_acesso",
		"Código Acesso":    "codigo_acesso",
		"ação-de-cobrança": "acao_de_cobranca",
		"order confirm":    "order_confirm",
		"já!?#ok":          "jaok",
	}
	for in, want := range cases {
		if got := normalizeTemplateName(in); got != want {
			t.Errorf("normalizeTemplateName(%q) = %q, want %q", in, got, want)
		}
	}
}
