package usage_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/usage"
	"github.com/stretchr/testify/assert"
)

func TestCountryOf(t *testing.T) {
	for phone, want := range map[string]string{
		"5511999990000": "55", "+55 (11) 99999-0000": "55", "14155550123": "1", "442071838750": "44",
		"351912345678": "351", "5491122334455": "54", "74951234567": "7", "": "", "000": "", "abc": "",
	} {
		assert.Equal(t, want, usage.CountryOf(phone), phone)
	}
}
