package models

import (
	"time"

	"github.com/google/uuid"
)

type SalesOpportunityStage string

const (
	SalesOpportunityStagePotencial      SalesOpportunityStage = "potencial"
	SalesOpportunityStageAbrirOrcamento SalesOpportunityStage = "abrir_orcamento"
	SalesOpportunityStageDirecionada    SalesOpportunityStage = "direcionada"
)

type SalesOpportunityStatus string

const (
	SalesOpportunityStatusAberta     SalesOpportunityStatus = "aberta"
	SalesOpportunityStatusConvertida SalesOpportunityStatus = "convertida"
	SalesOpportunityStatusPerdida    SalesOpportunityStatus = "perdida"
	SalesOpportunityStatusCancelada  SalesOpportunityStatus = "cancelada"
)

type SalesDirecionamento string

const (
	SalesDirecionamentoVisita   SalesDirecionamento = "visita"
	SalesDirecionamentoWhatsApp SalesDirecionamento = "whatsapp"
)

type SalesConversionSource string

const (
	SalesConversionSourceManual   SalesConversionSource = "manual"
	SalesConversionSourceXProcess SalesConversionSource = "xprocess"
)

// SalesLossReason is a closed list — validated in the handler, not just the DB.
type SalesLossReason string

const (
	SalesLossReasonClienteDesistiu    SalesLossReason = "cliente_desistiu"
	SalesLossReasonPreco              SalesLossReason = "preco"
	SalesLossReasonPrazo              SalesLossReason = "prazo"
	SalesLossReasonIndisponibilidade  SalesLossReason = "indisponibilidade"
	SalesLossReasonComprouConcorrente SalesLossReason = "comprou_concorrente"
	SalesLossReasonSemRetorno         SalesLossReason = "sem_retorno"
	SalesLossReasonProblemaComercial  SalesLossReason = "problema_comercial"
	SalesLossReasonOutro              SalesLossReason = "outro"
)

// ValidSalesLossReasons is the closed list, used by the handler to reject
// anything outside it with a 400 instead of trusting the request body.
var ValidSalesLossReasons = map[SalesLossReason]bool{
	SalesLossReasonClienteDesistiu:    true,
	SalesLossReasonPreco:              true,
	SalesLossReasonPrazo:              true,
	SalesLossReasonIndisponibilidade:  true,
	SalesLossReasonComprouConcorrente: true,
	SalesLossReasonSemRetorno:         true,
	SalesLossReasonProblemaComercial:  true,
	SalesLossReasonOutro:              true,
}

type SalesOpportunityEventType string

const (
	SalesOpportunityEventOpened               SalesOpportunityEventType = "opened"
	SalesOpportunityEventStageChanged         SalesOpportunityEventType = "stage_changed"
	SalesOpportunityEventDirecionamentoChanged SalesOpportunityEventType = "direcionamento_changed"
	SalesOpportunityEventConverted            SalesOpportunityEventType = "converted"
	SalesOpportunityEventLost                 SalesOpportunityEventType = "lost"
	SalesOpportunityEventCancelled            SalesOpportunityEventType = "cancelled"
	SalesOpportunityEventRetriggered          SalesOpportunityEventType = "retriggered"
)

type SalesOpportunityEventSource string

const (
	SalesOpportunityEventSourceManual   SalesOpportunityEventSource = "manual"
	SalesOpportunityEventSourceXProcess SalesOpportunityEventSource = "xprocess"
	SalesOpportunityEventSourceSystem   SalesOpportunityEventSource = "system"
)

// SalesOpportunity is the funnel entry created automatically when a customer
// selects a chatbot button configured with create_opportunity: true. See
// docs/superpowers/specs/2026-09-17-central-vendas-funil-entrega1-design.md §4.
type SalesOpportunity struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`

	// Unique per (organization_id, opportunity_number), not globally — two
	// orgs may repeat the same number on the same day (spec §4).
	OpportunityNumber string `gorm:"size:20;not null;uniqueIndex:idx_sales_opp_org_number" json:"opportunity_number"`

	ContactID uuid.UUID `gorm:"type:uuid;index;not null" json:"contact_id"`

	// Traceability only, same role as Occurrence.SourceTransferID.
	SourceTransferID *uuid.UUID `gorm:"type:uuid;index" json:"source_transfer_id,omitempty"`

	// Fixed at creation from Contact.AssignedUserID; reassigning the contact
	// later never moves this opportunity (spec §3).
	AssignedUserID *uuid.UUID `gorm:"type:uuid;index" json:"assigned_user_id,omitempty"`

	Stage  SalesOpportunityStage  `gorm:"size:20;not null;default:'potencial'" json:"stage"`
	Status SalesOpportunityStatus `gorm:"size:20;not null;default:'aberta'" json:"status"`

	Interest          string   `gorm:"type:text" json:"interest,omitempty"`
	EstimatedValue    *float64 `gorm:"type:numeric" json:"estimated_value,omitempty"`
	EstimatedQuantity *int     `json:"estimated_quantity,omitempty"`

	// Editable any time while status=aberta, in any stage — not itself a
	// transition. Required before entering "direcionada" (spec §5).
	Direcionamento *SalesDirecionamento `gorm:"size:20" json:"direcionamento,omitempty"`

	ConversionSource *SalesConversionSource `gorm:"size:20" json:"conversion_source,omitempty"`
	LossReason       *SalesLossReason       `gorm:"size:30" json:"loss_reason,omitempty"`
	LossNotes        string                  `gorm:"type:text" json:"loss_notes,omitempty"`

	OpenedAt time.Time `gorm:"autoCreateTime" json:"opened_at"`
	// Entry into the current stage — base of the 7-day SLA (spec §6).
	StageChangedAt time.Time  `gorm:"not null" json:"stage_changed_at"`
	ConvertedAt    *time.Time `json:"converted_at,omitempty"`
	LostAt         *time.Time `json:"lost_at,omitempty"`
	CancelledAt    *time.Time `json:"cancelled_at,omitempty"` // Entrega 2 only

	// SLA — mirrors Occurrence.SLA breach fields for the widget/board badge.
	SLABreached   bool       `gorm:"default:false" json:"sla_breached"`
	SLABreachedAt *time.Time `json:"sla_breached_at,omitempty"`

	Contact      *Contact `gorm:"foreignKey:ContactID" json:"contact,omitempty"`
	AssignedUser *User    `gorm:"foreignKey:AssignedUserID" json:"assigned_user,omitempty"`
}

func (SalesOpportunity) TableName() string { return "sales_opportunities" }

type SalesOpportunityEvent struct {
	BaseModel
	OrganizationID      uuid.UUID                    `gorm:"type:uuid;index;not null" json:"organization_id"`
	SalesOpportunityID  uuid.UUID                    `gorm:"type:uuid;index;not null" json:"sales_opportunity_id"`
	Type                SalesOpportunityEventType     `gorm:"size:30;not null" json:"type"`
	FromStage           *SalesOpportunityStage        `gorm:"size:20" json:"from_stage,omitempty"`
	ToStage             *SalesOpportunityStage        `gorm:"size:20" json:"to_stage,omitempty"`
	Source              SalesOpportunityEventSource   `gorm:"size:20;not null" json:"source"`
	CreatedByID         *uuid.UUID                    `gorm:"type:uuid" json:"created_by_id,omitempty"` // nil = system

	CreatedBy *User `gorm:"foreignKey:CreatedByID" json:"created_by,omitempty"`
}

func (SalesOpportunityEvent) TableName() string { return "sales_opportunity_events" }

// SalesOpportunityCounter holds the per-org, per-day opportunity_number
// sequence. Daily reset, unlike OccurrenceCounter's yearly one — sales
// volume outpaces occurrence volume (spec §3). Day is stored as "YYYYMMDD"
// so it doubles as the string embedded in opportunity_number.
type SalesOpportunityCounter struct {
	OrganizationID uuid.UUID `gorm:"type:uuid;primaryKey" json:"organization_id"`
	Day            string    `gorm:"size:8;primaryKey" json:"day"`
	LastSeq        int       `gorm:"not null;default:0" json:"last_seq"`
}

func (SalesOpportunityCounter) TableName() string { return "sales_opportunity_counters" }
