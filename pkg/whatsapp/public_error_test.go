package whatsapp

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPublicErrorText_CutsAnyRawBodyAndKeepsTheRest(t *testing.T) {
	raw := ParseMetaAPIError(502, []byte("<html>token=EAAB123 code 739104</html>"))
	assert.Equal(t, "API returned status 502", PublicErrorText(raw))
	wrapped := fmt.Errorf("send failed: %w", raw)
	assert.Equal(t, "send failed: API returned status 502", PublicErrorText(wrapped))

	meta := ParseMetaAPIError(400, []byte(`{"error":{"message":"Invalid parameter","code":100}}`))
	assert.Equal(t, meta.Error(), PublicErrorText(meta), "a parsed Meta error is already reduced to code and message")
	assert.Equal(t, "boom", PublicErrorText(errors.New("boom")))
	assert.Equal(t, "", PublicErrorText(nil))
}
