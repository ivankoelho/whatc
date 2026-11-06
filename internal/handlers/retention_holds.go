package handlers

import (
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// Legal holds: an administrator suspends expurgation for a protocol or a contact. While a hold is active
// nothing tied to its scope may be removed, by retention or by anyone: the engine skips it, and the
// ordinary delete actions refuse. A hold is never deleted; it is revoked, with who and why, and the history
// stays. The reason is mandatory both ways.

const minHoldReason = 10

// occurrenceHeld reports whether an active hold covers the protocol or its contact.
func (a *App) occurrenceHeld(orgID uuid.UUID, occ *models.Occurrence) bool {
	var n int64
	a.DB.Model(&models.RetentionHold{}).
		Where("organization_id = ? AND revoked_at IS NULL AND ((scope_type = ? AND scope_id = ?) OR (scope_type = ? AND scope_id = ?))",
			orgID, models.RetentionScopeOccurrence, occ.ID, models.RetentionScopeContact, occ.ContactID).Count(&n)
	return n > 0
}

// heldChatAttachmentSQL: the attachment `a` belongs to a case discussion covered by an active hold.
const heldChatAttachmentSQL = `EXISTS (SELECT 1 FROM chat_channels hch JOIN occurrences ho ON ho.id = hch.occurrence_id
	JOIN retention_holds hh ON hh.organization_id = a.organization_id AND hh.revoked_at IS NULL
		AND ((hh.scope_type = 'occurrence' AND hh.scope_id = ho.id) OR (hh.scope_type = 'contact' AND hh.scope_id = ho.contact_id))
	WHERE hch.id = a.channel_id)`

type holdView struct {
	models.RetentionHold
	CreatedBy string `json:"created_by"`
	RevokedBy string `json:"revoked_by,omitempty"`
	Active    bool   `json:"active"`
}

func (a *App) holdViews(holds []models.RetentionHold) []holdView {
	var ids []uuid.UUID
	for _, h := range holds {
		ids = append(ids, h.CreatedByID)
		if h.RevokedByID != nil {
			ids = append(ids, *h.RevokedByID)
		}
	}
	names := a.chatUserNames(ids)
	out := make([]holdView, len(holds))
	for i, h := range holds {
		v := holdView{RetentionHold: h, CreatedBy: names[h.CreatedByID], Active: h.RevokedAt == nil}
		if h.RevokedByID != nil {
			v.RevokedBy = names[*h.RevokedByID]
		}
		out[i] = v
	}
	return out
}

// ListRetentionHolds: ?status=active (default) | revoked | all.
func (a *App) ListRetentionHolds(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionRead)
	if err != nil {
		return nil
	}
	q := a.DB.Where("organization_id = ?", orgID)
	switch string(r.RequestCtx.QueryArgs().Peek("status")) {
	case "all":
	case "revoked":
		q = q.Where("revoked_at IS NOT NULL")
	default:
		q = q.Where("revoked_at IS NULL")
	}
	var holds []models.RetentionHold
	q.Order("created_at DESC").Limit(200).Find(&holds)
	return r.SendEnvelope(map[string]any{"holds": a.holdViews(holds)})
}

// CreateRetentionHold places a hold on a protocol (by id or number) or a contact.
func (a *App) CreateRetentionHold(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req struct {
		ScopeType      string `json:"scope_type"`
		ScopeID        string `json:"scope_id"`
		ProtocolNumber string `json:"protocol_number"`
		Reason         string `json:"reason"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	reason := strings.TrimSpace(req.Reason)
	if len([]rune(reason)) < minHoldReason {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "A justification is required (at least 10 characters)", nil, "")
	}

	var scopeID uuid.UUID
	var label string
	switch req.ScopeType {
	case models.RetentionScopeOccurrence:
		var occ models.Occurrence
		q := a.DB.Where("organization_id = ?", orgID)
		if n := strings.TrimSpace(req.ProtocolNumber); n != "" {
			q = q.Where("protocol_number = ?", n)
		} else if id, perr := uuid.Parse(req.ScopeID); perr == nil {
			q = q.Where("id = ?", id)
		} else {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Inform the protocol", nil, "")
		}
		if q.First(&occ).Error != nil {
			return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Protocol not found", nil, "")
		}
		scopeID, label = occ.ID, occ.ProtocolNumber
	case models.RetentionScopeContact:
		id, perr := uuid.Parse(req.ScopeID)
		if perr != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Inform the contact", nil, "")
		}
		var c models.Contact
		if a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&c).Error != nil {
			return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
		}
		scopeID, label = c.ID, c.ProfileName
		if label == "" {
			label = c.PhoneNumber
		}
	default:
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "scope_type must be occurrence or contact", nil, "")
	}

	var dup int64
	a.DB.Model(&models.RetentionHold{}).Where("organization_id = ? AND scope_type = ? AND scope_id = ? AND revoked_at IS NULL", orgID, req.ScopeType, scopeID).Count(&dup)
	if dup > 0 {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "This already has an active hold", nil, "")
	}
	hold := models.RetentionHold{ID: uuid.New(), OrganizationID: orgID, ScopeType: req.ScopeType, ScopeID: scopeID,
		ScopeLabel: truncateRunes(label, 200), Reason: reason, CreatedByID: userID}
	txErr := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&hold).Error; err != nil {
			return err
		}
		return tx.Create(&models.RetentionHoldEvent{ID: uuid.New(), OrganizationID: orgID, HoldID: hold.ID, Action: "placed", ActorID: userID, Detail: reason}).Error
	})
	if txErr != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to place the hold", nil, "")
	}
	a.logAudit(orgID, userID, "retention_hold", hold.ID, models.AuditActionCreated, nil,
		map[string]any{"scope_type": hold.ScopeType, "scope": hold.ScopeLabel, "reason": reason})
	return r.SendEnvelope(a.holdViews([]models.RetentionHold{hold})[0])
}

// RevokeRetentionHold ends a hold (it is kept, with who and why).
func (a *App) RevokeRetentionHold(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, perr := parsePathUUID(r, "id", "hold")
	if perr != nil {
		return nil
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	reason := strings.TrimSpace(req.Reason)
	if len([]rune(reason)) < minHoldReason {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "A justification is required (at least 10 characters)", nil, "")
	}
	var hold models.RetentionHold
	if a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&hold).Error != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Hold not found", nil, "")
	}
	now := time.Now()
	txErr := a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.RetentionHold{}).Where("id = ? AND revoked_at IS NULL", hold.ID).
			Updates(map[string]any{"revoked_at": now, "revoked_by_id": userID, "revoke_reason": reason})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound // already revoked
		}
		return tx.Create(&models.RetentionHoldEvent{ID: uuid.New(), OrganizationID: orgID, HoldID: hold.ID, Action: "revoked", ActorID: userID, Detail: reason}).Error
	})
	if txErr == gorm.ErrRecordNotFound {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "This hold was already revoked", nil, "")
	}
	if txErr != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to revoke the hold", nil, "")
	}
	a.logAudit(orgID, userID, "retention_hold", hold.ID, models.AuditActionUpdated, map[string]any{"active": true},
		map[string]any{"active": false, "reason": reason, "scope": hold.ScopeLabel})
	_ = a.DB.First(&hold, "id = ?", hold.ID)
	return r.SendEnvelope(a.holdViews([]models.RetentionHold{hold})[0])
}

type retentionHistoryEntry struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"` // policy | hold_placed | hold_revoked | run
	Actor   string    `json:"actor,omitempty"`
	Summary string    `json:"summary"`
}

// RetentionHistory merges what an administrator needs to review: policy changes, hold events and purge runs.
func (a *App) RetentionHistory(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionRead)
	if err != nil {
		return nil
	}
	out := []retentionHistoryEntry{}

	var logs []models.AuditLog
	a.DB.Where("organization_id = ? AND resource_type = ?", orgID, "retention_policy").Order("created_at DESC").Limit(100).Find(&logs)
	for _, l := range logs {
		summary := "policy changed"
		for _, c := range l.Changes {
			if m, ok := c.(map[string]any); ok && m["field"] == "category" {
				summary = "policy changed: " + toString(m["new_value"])
			}
		}
		out = append(out, retentionHistoryEntry{At: l.CreatedAt, Kind: "policy", Actor: l.UserName, Summary: summary})
	}
	var evs []models.RetentionHoldEvent
	a.DB.Where("organization_id = ?", orgID).Order("created_at DESC").Limit(100).Find(&evs)
	var holdIDs, actorIDs []uuid.UUID
	for _, e := range evs {
		holdIDs = append(holdIDs, e.HoldID)
		actorIDs = append(actorIDs, e.ActorID)
	}
	labels := map[uuid.UUID]string{}
	var hs []models.RetentionHold
	if len(holdIDs) > 0 {
		a.DB.Where("organization_id = ? AND id IN ?", orgID, holdIDs).Find(&hs)
	}
	for _, h := range hs {
		labels[h.ID] = h.ScopeType + " " + h.ScopeLabel
	}
	names := a.chatUserNames(actorIDs)
	for _, e := range evs {
		kind := "hold_placed"
		if e.Action == "revoked" {
			kind = "hold_revoked"
		}
		out = append(out, retentionHistoryEntry{At: e.CreatedAt, Kind: kind, Actor: names[e.ActorID], Summary: labels[e.HoldID] + ": " + e.Detail})
	}
	var runs []models.RetentionRun
	a.DB.Where("organization_id = ?", orgID).Order("started_at DESC").Limit(50).Find(&runs)
	for _, run := range runs {
		out = append(out, retentionHistoryEntry{At: run.StartedAt, Kind: "run", Summary: run.Category + " (" + run.Mode + "): " + run.Status})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > 100 {
		out = out[:100]
	}
	return r.SendEnvelope(map[string]any{"history": out})
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
