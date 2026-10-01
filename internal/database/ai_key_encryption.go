package database

import (
	"fmt"

	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// EncryptChatbotAIKeys encrypts, in place, every AI provider API key that is
// still stored as plaintext in chatbot_settings.ai_api_key (rows without the
// "enc:" prefix, soft-deleted rows included).
//
// Safe by construction:
//   - idempotent: already-encrypted rows are never touched, so re-running is a no-op;
//   - verified: each value is decrypted again and compared before it is written;
//   - compare-and-set: the UPDATE only applies if the column still holds the
//     plaintext that was read, so a concurrent save of a new key is not overwritten;
//   - never logs or returns a key, only counts;
//   - with an empty encryptionKey nothing is changed (crypto.Encrypt would be a
//     no-op) and a warning explains why, instead of pretending it worked.
//
// Returns how many rows were encrypted and how many plaintext rows remain.
func EncryptChatbotAIKeys(db *gorm.DB, encryptionKey string, log logf.Logger) (encrypted, remaining int, err error) {
	// With debug logging on, GORM prints statements with their arguments. These
	// statements carry plaintext keys, so they never go through the logger.
	db = db.Session(&gorm.Session{Logger: logger.Discard})

	var rows []struct {
		ID       string `gorm:"column:id"`
		AIAPIKey string `gorm:"column:ai_api_key"`
	}
	if err := db.Raw(`SELECT id::text AS id, ai_api_key FROM chatbot_settings
		WHERE ai_api_key IS NOT NULL AND ai_api_key <> '' AND ai_api_key NOT LIKE 'enc:%'`).Scan(&rows).Error; err != nil {
		return 0, 0, fmt.Errorf("failed to list plaintext AI keys: %w", err)
	}
	if len(rows) == 0 {
		return 0, 0, nil
	}
	if encryptionKey == "" {
		log.Warn("app.encryption_key is empty: AI API keys stay in plaintext. Set it (32+ chars) and re-run the migration",
			"plaintext_keys", len(rows))
		return 0, len(rows), nil
	}

	for _, row := range rows {
		enc, err := crypto.Encrypt(row.AIAPIKey, encryptionKey)
		if err != nil {
			return encrypted, len(rows) - encrypted, fmt.Errorf("failed to encrypt an AI key: %w", err)
		}
		if dec, err := crypto.Decrypt(enc, encryptionKey); err != nil || dec != row.AIAPIKey {
			return encrypted, len(rows) - encrypted, fmt.Errorf("AI key round-trip check failed for settings row %s, nothing written for it", row.ID)
		}
		res := db.Exec(`UPDATE chatbot_settings SET ai_api_key = ? WHERE id = ?::uuid AND ai_api_key = ?`, enc, row.ID, row.AIAPIKey)
		if res.Error != nil {
			return encrypted, len(rows) - encrypted, fmt.Errorf("failed to store an encrypted AI key: %w", res.Error)
		}
		encrypted += int(res.RowsAffected)
	}
	log.Info("Encrypted AI API keys at rest", "encrypted", encrypted)
	return encrypted, len(rows) - encrypted, nil
}
