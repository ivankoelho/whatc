package models

import "github.com/google/uuid"

// Knowledge document sources. manual_html only comes from the CLI import.
const (
	KnowledgeSourceFAQ        = "faq"
	KnowledgeSourceArticle    = "article"
	KnowledgeSourceProcess    = "process"
	KnowledgeSourceText       = "text"
	KnowledgeSourceMarkdown   = "markdown"
	KnowledgeSourceManualHTML = "manual_html"
)

// Knowledge document visibility is DERIVED from the scope, never typed by the
// caller: a department document needs that department, a unit document needs
// that unit, otherwise it is global to the organization.
const (
	KnowledgeVisibilityOrganization = "organization"
	KnowledgeVisibilityUnit         = "unit"
	KnowledgeVisibilityDepartment   = "department"
)

const (
	KnowledgeStatusActive   = "active"
	KnowledgeStatusArchived = "archived"
)

// KnowledgeDocument is one piece of corporate knowledge (FAQ, article,
// process, text, imported manual section). Body holds the original text; the
// searchable copy lives on the chunks.
type KnowledgeDocument struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	UnitID         *uuid.UUID `gorm:"type:uuid;index" json:"unit_id,omitempty"`
	DepartmentID   *uuid.UUID `gorm:"type:uuid;index" json:"department_id,omitempty"`
	Visibility     string     `gorm:"size:20;not null;default:organization" json:"visibility"`
	SourceType     string     `gorm:"size:20;not null" json:"source_type"`
	Title          string     `gorm:"size:500;not null" json:"title"`
	// Origin identifies where the text came from (e.g. manual/Guia.html#s-fila);
	// unique per organization when set, so a re-import replaces instead of duplicating.
	Origin      string     `gorm:"size:500" json:"origin,omitempty"`
	Body        string     `gorm:"type:text" json:"body,omitempty"`
	ContentHash string     `gorm:"size:64" json:"content_hash,omitempty"`
	Status      string     `gorm:"size:20;not null;default:active" json:"status"`
	CreatedByID *uuid.UUID `gorm:"type:uuid" json:"created_by_id,omitempty"`
	UpdatedByID *uuid.UUID `gorm:"type:uuid" json:"updated_by_id,omitempty"`
}

func (KnowledgeDocument) TableName() string { return "knowledge_documents" }

// KnowledgeChunk is the retrievable unit. Scope and status are copied from the
// document so the scope filter is a plain WHERE on this table (no JOIN) applied
// before ranking and LIMIT; they are updated in the same transaction as the document.
//
// Heading/Content keep the original text for display and citation; SearchHeading
// and SearchText are the accent-folded copies the full-text index is built from
// (a generated tsvector column, search_vector, is created by the index SQL).
type KnowledgeChunk struct {
	ID             uuid.UUID  `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	DocumentID     uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_knowledge_chunks_doc_idx" json:"document_id"`
	OrganizationID uuid.UUID  `gorm:"type:uuid;not null" json:"organization_id"`
	UnitID         *uuid.UUID `gorm:"type:uuid" json:"unit_id,omitempty"`
	DepartmentID   *uuid.UUID `gorm:"type:uuid" json:"department_id,omitempty"`
	Visibility     string     `gorm:"size:20;not null" json:"visibility"`
	Status         string     `gorm:"size:20;not null" json:"status"`
	ChunkIndex     int        `gorm:"not null;uniqueIndex:idx_knowledge_chunks_doc_idx" json:"chunk_index"`
	Heading        string     `gorm:"type:text" json:"heading"`
	Content        string     `gorm:"type:text;not null" json:"content"`
	SearchHeading  string     `gorm:"type:text" json:"-"`
	SearchText     string     `gorm:"type:text" json:"-"`
	Metadata       JSONB      `gorm:"type:jsonb;default:'{}'" json:"metadata"`
}

func (KnowledgeChunk) TableName() string { return "knowledge_chunks" }
