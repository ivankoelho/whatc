package whatsapp_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/stretchr/testify/assert"
)

// legacyOTPButtonParams is the block that used to live inline in
// handlers.SendTemplateMessage, kept verbatim as the reference behaviour.
func legacyOTPButtonParams(buttons []any, code string, existing map[string]string) map[string]string {
	buttonParams := existing
	if len(buttonParams) == 0 {
		if code != "" {
			for i, raw := range buttons {
				if btn, ok := raw.(map[string]any); ok {
					btnType, _ := btn["type"].(string)
					if strings.EqualFold(btnType, "OTP") {
						if buttonParams == nil {
							buttonParams = make(map[string]string)
						}
						buttonParams[fmt.Sprintf("%d", i)] = code
						break
					}
				}
			}
		}
	}
	return buttonParams
}

func TestOTPButtonParams_MatchesLegacyInlineBehaviour(t *testing.T) {
	otp := map[string]any{"type": "OTP", "text": "Copy code", "otp_type": "COPY_CODE"}
	quick := map[string]any{"type": "QUICK_REPLY", "text": "ok"}
	cases := map[string]struct {
		buttons []any
		code    string
	}{
		"otp first":            {[]any{otp}, "123456"},
		"otp after other":      {[]any{quick, otp}, "123456"},
		"lowercase type":       {[]any{map[string]any{"type": "otp"}}, "123456"},
		"two otp, first wins":  {[]any{otp, otp}, "123456"},
		"no code":              {[]any{otp}, ""},
		"no otp button":        {[]any{quick}, "123456"},
		"no buttons":           {nil, "123456"},
		"non-map entries":      {[]any{"junk", 42, nil, otp}, "123456"},
		"button without type":  {[]any{map[string]any{"text": "x"}}, "123456"},
		"type is not a string": {[]any{map[string]any{"type": 7}}, "123456"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := whatsapp.OTPButtonParams(c.buttons, c.code)
			want := legacyOTPButtonParams(c.buttons, c.code, nil)
			assert.Equal(t, len(want), len(got))
			if len(want) > 0 {
				assert.Equal(t, want, got)
			}
		})
	}
}

// Full structure of the OTP button component. Meta answers 131008 when the
// button parameter is missing or shaped differently.
func TestOTPButton_FullComponentStructure(t *testing.T) {
	otp := map[string]any{"type": "OTP", "text": "Copy code", "otp_type": "COPY_CODE"}
	buttons := []any{otp}

	got := whatsapp.ButtonURLParamsToComponents(whatsapp.OTPButtonParams(buttons, "123456"), buttons)
	assert.Equal(t, []map[string]any{{
		"type":     "button",
		"sub_type": "url",
		"index":    "0",
		"parameters": []map[string]any{
			{"type": "text", "text": "123456"},
		},
	}}, got)

	// One-tap / zero-tap buttons take no parameter from us.
	for _, kind := range []string{"ONE_TAP", "ZERO_TAP"} {
		b := []any{map[string]any{"type": "OTP", "otp_type": kind}}
		assert.Empty(t, whatsapp.ButtonURLParamsToComponents(whatsapp.OTPButtonParams(b, "123456"), b), kind)
	}

	// No OTP button or no code: no button component at all.
	assert.Empty(t, whatsapp.ButtonURLParamsToComponents(whatsapp.OTPButtonParams([]any{map[string]any{"type": "QUICK_REPLY"}}, "123456"), []any{map[string]any{"type": "QUICK_REPLY"}}))
	assert.Empty(t, whatsapp.ButtonURLParamsToComponents(whatsapp.OTPButtonParams(buttons, ""), buttons))
}
