package models

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
)

// XProcessIntegration holds one organization's credential for the customer's
// X2 ERP API (design §4). One row per organization — OrganizationID is
// unique, mirroring how WhatsAppAccount scopes its own encrypted secrets.
type XProcessIntegration struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;uniqueIndex;not null" json:"organization_id"`
	BaseURL        string    `gorm:"size:255;not null" json:"base_url"`
	// APIKey is encrypted at rest via EncryptSecrets/DecryptSecrets, same
	// pattern as WhatsAppAccount.AccessToken. Never serialize this field
	// back to the frontend in plaintext — handlers must omit it, not just
	// rely on json:"-" (a caller could still read it straight off the
	// struct), so the "-" tag here is a second layer, not the only one.
	APIKey   string `gorm:"type:text" json:"-"`
	IsActive bool   `gorm:"default:true" json:"is_active"`
}

func (XProcessIntegration) TableName() string { return "xprocess_integrations" }

// EncryptSecrets encrypts APIKey in place before a Create/Save.
func (x *XProcessIntegration) EncryptSecrets(encryptionKey string) error {
	return crypto.EncryptFields(encryptionKey, &x.APIKey)
}

// DecryptSecrets decrypts APIKey in place after a read from the DB.
func (x *XProcessIntegration) DecryptSecrets(encryptionKey string) {
	crypto.DecryptFields(encryptionKey, &x.APIKey)
}
