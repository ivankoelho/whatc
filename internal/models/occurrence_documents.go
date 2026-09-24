package models

import (
	"time"

	"github.com/google/uuid"
)

// Purchase document kinds. Only a few stores print the NF at the till; the
// others hand the customer a cupom fiscal and the NF is issued later at the
// warehouse, so a case may only have the cupom (identified by the order number).
const (
	OccurrenceDocumentNF    = "nf"
	OccurrenceDocumentCupom = "cupom"
)

// OccurrenceDocument is one purchase document (NF or cupom/pedido) attached to
// an occurrence, with the products it covers and, optionally, the file the
// customer sent. An occurrence may have several.
type OccurrenceDocument struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	OccurrenceID   uuid.UUID  `gorm:"type:uuid;index;not null" json:"occurrence_id"`
	Type           string     `gorm:"size:10;not null" json:"type"`
	Number         string     `gorm:"size:60;not null" json:"number"`
	PurchaseDate   *time.Time `json:"purchase_date,omitempty"`
	// Items is a list of {code, description, quantity}. Stored as JSON: nothing
	// queries individual products yet.
	Items JSONBArray `gorm:"type:jsonb;not null;default:'[]'" json:"items"`

	AttachmentPath string `gorm:"size:500" json:"-"`
	AttachmentName string `gorm:"size:255" json:"attachment_name,omitempty"`
	AttachmentMime string `gorm:"size:100" json:"attachment_mime,omitempty"`

	CreatedByID *uuid.UUID `gorm:"type:uuid" json:"created_by_id,omitempty"`
}

func (OccurrenceDocument) TableName() string { return "occurrence_documents" }
