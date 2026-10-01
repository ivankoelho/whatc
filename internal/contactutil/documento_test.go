package contactutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeDocumento(t *testing.T) {
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"empty is allowed", "", "", true},
		{"only mask is empty", " .-/ ", "", true},
		{"valid CPF with mask", "529.982.247-25", "52998224725", true},
		{"valid CPF without mask", "52998224725", "52998224725", true},
		{"valid CNPJ with mask", "07.378.783/0001-90", "07378783000190", true},
		{"valid CNPJ without mask", "07378783000190", "07378783000190", true},
		{"CPF with wrong check digit", "529.982.247-26", "", false},
		{"CNPJ with wrong check digit", "07378783000191", "", false},
		{"CPF all equal digits", "111.111.111-11", "", false},
		{"CNPJ all equal digits", "00000000000000", "", false},
		{"9 digits", "123456789", "", false},
		{"12 digits", "123456789012", "", false},
		{"15 digits", "123456789012345", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeDocumento(c.in)
			if !c.ok {
				assert.ErrorIs(t, err, ErrInvalidDocumento)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}
