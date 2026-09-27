package models_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestXProcessIntegration_EncryptDecryptSecrets(t *testing.T) {
	key := "test-encryption-key-for-models-longer-than-32-chars"
	integ := &models.XProcessIntegration{
		BaseURL: "https://api.atacadaodospisos.com.br",
		APIKey:  "plain-api-key",
	}
	require.NoError(t, integ.EncryptSecrets(key))
	assert.NotEqual(t, "plain-api-key", integ.APIKey, "api key must not stay in plaintext after EncryptSecrets")

	integ.DecryptSecrets(key)
	assert.Equal(t, "plain-api-key", integ.APIKey)
}

func TestXProcessIntegration_TableName(t *testing.T) {
	assert.Equal(t, "xprocess_integrations", models.XProcessIntegration{}.TableName())
}
