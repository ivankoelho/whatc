package handlers

import (
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Forwarding (SAC -> team): the artifacts written together with the case.
// ---------------------------------------------------------------------------

// forwardArtifacts returns the writes that accompany a case forwarded to a
// team. They run in the same transaction as the case, so a forwarded case is
// never left without its trail:
//   - a "forwarded" timeline event (who asked which team, about which order);
//   - the requester as a participant who may comment, when they are not on
//     the receiving team, so the SAC keeps following and collaborating;
//   - the customer notification PREPARED (account, eligibility, idempotency
//     key) but not sent: dispatch belongs to the next phase.
func (a *App) forwardArtifacts(userID uuid.UUID, contact *models.Contact, requesterInTeam bool) func(tx *gorm.DB, occ *models.Occurrence) error {
	return func(tx *gorm.DB, occ *models.Occurrence) error {
		meta := models.JSONB{"requester_id": userID.String()}
		if occ.TeamID != nil {
			meta["team_id"] = occ.TeamID.String()
		}
		if occ.UnitID != nil {
			meta["unit_id"] = occ.UnitID.String()
		}
		if occ.OrderRef != "" {
			meta["order_ref"] = occ.OrderRef
		}
		if occ.SourceTransferID != nil {
			meta["source_transfer_id"] = occ.SourceTransferID.String()
		}
		if err := tx.Create(&models.OccurrenceEvent{
			OrganizationID: occ.OrganizationID, OccurrenceID: occ.ID,
			Type: models.OccurrenceEventForwarded, Content: occ.Title, Metadata: meta, CreatedByID: &userID,
		}).Error; err != nil {
			return err
		}

		if !requesterInTeam {
			if err := tx.Create(&models.OccurrenceParticipant{
				OrganizationID: occ.OrganizationID, OccurrenceID: occ.ID, UserID: userID, AddedByID: &userID,
				Capabilities: []string{models.OccurrenceCapComment},
			}).Error; err != nil {
				return err
			}
		}

		account := a.resolveOccurrenceChannel(tx, occ.OrganizationID, occ.UnitID)
		n := models.OccurrenceNotification{
			OrganizationID: occ.OrganizationID, OccurrenceID: occ.ID, Kind: "created",
			WhatsAppAccount: account, IdempotencyKey: "occ:" + occ.ID.String() + ":created",
			Status: "prepared", Reason: "dispatch_not_enabled", Eligible: false,
		}
		switch {
		case account == "":
			n.Status, n.Reason = "skipped", "no_channel_configured"
		case contact == nil || strings.TrimSpace(contact.PhoneNumber) == "":
			n.Status, n.Reason = "skipped", "contact_without_phone"
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&n).Error
	}
}

// resolveOccurrenceChannel returns the WhatsApp account that talks to
// customers about cases of the unit: the unit's own configuration, else the
// organization-wide one, else "". The account must still exist.
func (a *App) resolveOccurrenceChannel(tx *gorm.DB, orgID uuid.UUID, unitID *uuid.UUID) string {
	lookup := func(q *gorm.DB) string {
		var cfg models.OccurrenceChannelConfig
		if err := q.First(&cfg).Error; err != nil {
			return ""
		}
		var n int64
		tx.Model(&models.WhatsAppAccount{}).Where("organization_id = ? AND name = ?", orgID, cfg.WhatsAppAccount).Count(&n)
		if n == 0 {
			return ""
		}
		return cfg.WhatsAppAccount
	}
	if unitID != nil {
		if acc := lookup(tx.Where("organization_id = ? AND purpose = ? AND unit_id = ?", orgID, "logistics", *unitID)); acc != "" {
			return acc
		}
	}
	return lookup(tx.Where("organization_id = ? AND purpose = ? AND unit_id IS NULL", orgID, "logistics"))
}

type channelConfigRequest struct {
	UnitID          string `json:"unit_id"`
	WhatsAppAccount string `json:"whatsapp_account"`
}

// ListOccurrenceChannelConfigs shows which account serves which unit.
func (a *App) ListOccurrenceChannelConfigs(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionRead)
	if err != nil {
		return nil
	}
	var cfgs []models.OccurrenceChannelConfig
	a.DB.Where("organization_id = ?", orgID).Order("created_at").Find(&cfgs)
	return r.SendEnvelope(map[string]any{"configs": cfgs})
}

// SetOccurrenceChannelConfig sets the account for a unit (or, without unit_id,
// the organization default). Configuration only: nothing is sent.
func (a *App) SetOccurrenceChannelConfig(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceAccounts, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req channelConfigRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	var n int64
	a.DB.Model(&models.WhatsAppAccount{}).Where("organization_id = ? AND name = ?", orgID, req.WhatsAppAccount).Count(&n)
	if n == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Unknown WhatsApp account", nil, "")
	}
	cfg := models.OccurrenceChannelConfig{OrganizationID: orgID, Purpose: "logistics", WhatsAppAccount: req.WhatsAppAccount}
	q := a.DB.Where("organization_id = ? AND purpose = ?", orgID, "logistics")
	if req.UnitID != "" {
		id, err := uuid.Parse(req.UnitID)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid unit_id", nil, "")
		}
		if _, err := findByIDAndOrg[models.Unit](a.DB, r, id, orgID, "Unit"); err != nil {
			return nil
		}
		cfg.UnitID = &id
		q = q.Where("unit_id = ?", id)
	} else {
		q = q.Where("unit_id IS NULL")
	}
	var existing models.OccurrenceChannelConfig
	if err := q.First(&existing).Error; err == nil {
		a.DB.Model(&existing).Update("whats_app_account", req.WhatsAppAccount)
		cfg = existing
		cfg.WhatsAppAccount = req.WhatsAppAccount
	} else if err := a.DB.Create(&cfg).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save channel config", nil, "")
	}
	a.logAudit(orgID, userID, "occurrence_channel_config", cfg.ID, models.AuditActionUpdated, nil, cfg)
	return r.SendEnvelope(cfg)
}


type forwardTarget struct {
	ID       uuid.UUID  `json:"id"`
	Name     string     `json:"name"`
	UnitID   *uuid.UUID `json:"unit_id,omitempty"`
	UnitName string     `json:"unit_name,omitempty"`
}

// ListForwardTargets lists the teams a case may be forwarded to. Anyone who can
// open cases needs it, and the regular teams listing only shows a user their
// own teams, so this returns just what the picker needs (team and store names)
// and nothing about members.
func (a *App) ListForwardTargets(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	var teams []models.Team
	a.DB.Where("organization_id = ? AND is_active = ?", orgID, true).Order("name").Find(&teams)
	units := map[uuid.UUID]string{}
	var us []models.Unit
	a.DB.Select("id, name").Where("organization_id = ?", orgID).Find(&us)
	for _, u := range us {
		units[u.ID] = u.Name
	}
	out := make([]forwardTarget, 0, len(teams))
	for _, t := range teams {
		ft := forwardTarget{ID: t.ID, Name: t.Name, UnitID: t.UnitID}
		if t.UnitID != nil {
			ft.UnitName = units[*t.UnitID]
		}
		out = append(out, ft)
	}
	return r.SendEnvelope(map[string]any{"targets": out})
}
