package handlers

import (
	"errors"

	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
)

// ErrAIKeyUnavailable means the stored AI API key cannot be turned into a usable
// credential (no key, or it is encrypted and cannot be decrypted).
var ErrAIKeyUnavailable = errors.New("AI API key is not available")

// encryptAIKey encrypts a key supplied by an admin before it is stored. With no
// app.encryption_key configured the value is stored unencrypted (same behaviour
// as the other secrets) and a warning is logged so it is not silent.
func (a *App) encryptAIKey(plain string) (string, error) {
	if a.Config.App.EncryptionKey == "" {
		a.Log.Warn("app.encryption_key is empty: the AI API key is being stored unencrypted")
	}
	return crypto.Encrypt(plain, a.Config.App.EncryptionKey)
}

// resolveAIAPIKey is the ONLY place an AI API key is decrypted. settings.AI.APIKey
// (database row, Redis cache, in-memory structs, logs) always holds the stored,
// encrypted form; the plaintext lives just long enough to build the provider call.
//
// A value that is encrypted but cannot be decrypted (wrong or missing
// encryption key) is an error: sending the "enc:..." text to a provider as if it
// were the key would both fail and disclose ciphertext to a third party.
func (a *App) resolveAIAPIKey(settings *models.ChatbotSettings) (string, error) {
	stored := settings.AI.APIKey
	if stored == "" {
		return "", ErrAIKeyUnavailable
	}
	encKey := a.Config.App.EncryptionKey
	if crypto.IsEncrypted(stored) && encKey == "" {
		a.Log.Error("AI API key is encrypted but app.encryption_key is not configured")
		return "", ErrAIKeyUnavailable
	}
	plain, err := crypto.Decrypt(stored, encKey)
	if err != nil || plain == "" {
		a.Log.Error("Failed to decrypt AI API key (was app.encryption_key changed?)")
		return "", ErrAIKeyUnavailable
	}
	return plain, nil
}
