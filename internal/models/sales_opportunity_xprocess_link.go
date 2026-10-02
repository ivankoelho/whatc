package models

import (
	"time"

	"github.com/google/uuid"
)

// SalesOpportunityXProcessLink tracks one attempt to reconcile a
// SalesOpportunity with an X2 pedido (design §4). Deliberately NOT 1:1 with
// the opportunity: there can be several rows over time, but at most one
// with ResolvedAt == nil at any moment — see UpdateSalesOpportunityXProcessLink
// (internal/handlers) for the rule that enforces this.
type SalesOpportunityXProcessLink struct {
	BaseModel
	OrganizationID     uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	SalesOpportunityID uuid.UUID `gorm:"type:uuid;index;not null" json:"sales_opportunity_id"`

	// NumPedido/Documento are copied from the opportunity at registration
	// time, not read live from it — a resolved link's audit trail must
	// reflect what was actually checked, even if the opportunity's own
	// fields later point at a newer link.
	NumPedido string `gorm:"size:20;not null" json:"num_pedido"`
	Documento string `gorm:"size:14;not null" json:"documento"` // digits only, 11 (CPF) or 14 (CNPJ)

	CodEmpresa  *string `gorm:"size:20" json:"cod_empresa,omitempty"`
	CodVendedor *string `gorm:"size:20" json:"cod_vendedor,omitempty"`
	// column:status_xprocess is explicit because GORM's default namer splits
	// "StatusXProcess" as "status_x_process" (verified against
	// gorm.io/gorm/schema.NamingStrategy — the lone capital X before a
	// mixed-case word still counts as its own segment). Same class of bug as
	// Contact.CPFCNPJ (commit 00e573c) and the existing
	// User.XProcessSellerCode field below, which already carries this same
	// explicit override for the same reason.
	StatusXProcess *string    `gorm:"column:status_xprocess;size:20" json:"status_xprocess,omitempty"`
	ValorVendido   *float64   `gorm:"type:numeric" json:"valor_vendido,omitempty"`
	Itens          JSONBArray `gorm:"type:jsonb" json:"itens,omitempty"`
	// ValorFrete is the pedido's freight (sum of the lines' vl_frete), recorded apart
	// from ValorVendido, which never includes it. Informational.
	ValorFrete *float64 `gorm:"type:numeric" json:"valor_frete,omitempty"`

	// LinkSource says who created the link: "agent" (typed by a user) or "auto"
	// (found by the discovery job). MatchReason explains an automatic match.
	LinkSource  string `gorm:"size:10;not null;default:'agent'" json:"link_source"`
	MatchReason string `gorm:"type:text" json:"match_reason,omitempty"`

	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	// FirstClosedAt is written exactly once, on the first round the job
	// sees status=FECHADO for this link. Never overwritten afterwards —
	// the 7-day grace period (design §5) is always computed from this
	// original value, never from LastCheckedAt.
	FirstClosedAt *time.Time `json:"first_closed_at,omitempty"`
	// ConsecutiveNotFound counts JOB ROUNDS that returned 404, not calendar
	// days since registration (design §4) — resets to 0 on any 200.
	ConsecutiveNotFound int `gorm:"default:0" json:"consecutive_not_found"`
	// ResolvedAt nil means the job keeps selecting this link. See design §5's
	// resolved_at table for the exact per-status rule.
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

func (SalesOpportunityXProcessLink) TableName() string { return "sales_opportunity_xprocess_links" }
