package database_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const aiTestEncKey = "test-encryption-key-for-database-tests-32chars"

func insertAIKeyRow(t *testing.T, db *gorm.DB, orgID uuid.UUID, account, key string, softDeleted bool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	require.NoError(t, db.Exec(`INSERT INTO chatbot_settings (id, organization_id, whats_app_account, ai_api_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, NOW(), NOW())`, id, orgID, account, key).Error)
	if softDeleted {
		require.NoError(t, db.Exec(`UPDATE chatbot_settings SET deleted_at = NOW() WHERE id = ?`, id).Error)
	}
	return id
}

func aiKeyOf(t *testing.T, db *gorm.DB, id uuid.UUID) string {
	t.Helper()
	var v string
	require.NoError(t, db.Raw(`SELECT ai_api_key FROM chatbot_settings WHERE id = ?`, id).Scan(&v).Error)
	return v
}

func TestEncryptChatbotAIKeys_EncryptsPlaintextOnlyAndIsIdempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	plain := insertAIKeyRow(t, db, org.ID, "", "sk-plain-1", false)
	deleted := insertAIKeyRow(t, db, org.ID, "acc-deleted", "sk-plain-deleted", true)
	already, err := crypto.Encrypt("sk-already", aiTestEncKey)
	require.NoError(t, err)
	encRow := insertAIKeyRow(t, db, org.ID, "acc-enc", already, false)
	empty := insertAIKeyRow(t, db, org.ID, "acc-empty", "", false)

	n, remaining, err := database.EncryptChatbotAIKeys(db, aiTestEncKey, testutil.NopLogger())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, 2, "the plaintext rows (soft-deleted included) are encrypted")
	assert.Zero(t, remaining)

	for id, want := range map[uuid.UUID]string{plain: "sk-plain-1", deleted: "sk-plain-deleted"} {
		stored := aiKeyOf(t, db, id)
		assert.True(t, crypto.IsEncrypted(stored))
		dec, err := crypto.Decrypt(stored, aiTestEncKey)
		require.NoError(t, err)
		assert.Equal(t, want, dec, "the value round-trips exactly")
	}
	assert.Equal(t, already, aiKeyOf(t, db, encRow), "an already-encrypted row is not touched")
	assert.Equal(t, "", aiKeyOf(t, db, empty))

	before := aiKeyOf(t, db, plain)
	n2, _, err := database.EncryptChatbotAIKeys(db, aiTestEncKey, testutil.NopLogger())
	require.NoError(t, err)
	assert.Zero(t, n2, "second run is a no-op")
	assert.Equal(t, before, aiKeyOf(t, db, plain), "no double encryption")
}

func TestEncryptChatbotAIKeys_EmptyEncryptionKeyChangesNothing(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	id := insertAIKeyRow(t, db, org.ID, "", "sk-keep-plain", false)

	n, remaining, err := database.EncryptChatbotAIKeys(db, "", testutil.NopLogger())
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.GreaterOrEqual(t, remaining, 1, "it reports what is still plaintext instead of pretending")
	assert.Equal(t, "sk-keep-plain", aiKeyOf(t, db, id))
}
