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

const (
	automationEventForwarded = "forwarded"
	automationPurpose        = "logistics"
	notificationKindCreated  = "created"
)

// ---------------------------------------------------------------------------
// Forwarding (SAC -> team): the artifacts written together with the case.
// ---------------------------------------------------------------------------

// forwardArtifacts returns the writes that accompany a case forwarded to a
// team. They run in the same transaction as the case, so a forwarded case is
// never left without its trail:
//   - a "forwarded" timeline event (who asked which team, about which order);
//   - the requester as a participant who may comment, when they are not on the
//     receiving team, so the SAC keeps following and collaborating;
//   - the customer communication INTENT (see newForwardNotification). The send
//     itself happens later, in the worker, never inside this transaction.
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
		return a.newForwardNotification(tx, occ, contact)
	}
}

// resolveAutomation returns the configuration that governs (unit, team, event):
// the team's own, else the unit's, else the organization's. A more specific row
// prevails even when inactive, and a missing one is never replaced by
// "some other account": no row means no automation.
func resolveAutomation(tx *gorm.DB, orgID uuid.UUID, unitID, teamID *uuid.UUID, event string) *models.OccurrenceChannelConfig {
	base := func() *gorm.DB {
		return tx.Where("organization_id = ? AND purpose = ? AND event = ?", orgID, automationPurpose, event)
	}
	var cfg models.OccurrenceChannelConfig
	if teamID != nil && base().Where("team_id = ?", *teamID).First(&cfg).Error == nil {
		return &cfg
	}
	if unitID != nil && base().Where("unit_id = ? AND team_id IS NULL", *unitID).First(&cfg).Error == nil {
		return &cfg
	}
	if base().Where("unit_id IS NULL AND team_id IS NULL").First(&cfg).Error == nil {
		return &cfg
	}
	return nil
}

// newForwardNotification records the communication intent for a forwarded case.
// Idempotent: the key is stable per case and kind, so a repeated event writes
// nothing new. It decides only whether the intent is sendable; every send-time
// rule (template approved, parameters, opt-out) is checked again by the worker,
// because the world may change between now and then.
func (a *App) newForwardNotification(tx *gorm.DB, occ *models.Occurrence, contact *models.Contact) error {
	n := models.OccurrenceNotification{
		OrganizationID: occ.OrganizationID, OccurrenceID: occ.ID, Kind: notificationKindCreated,
		IdempotencyKey: "occ:" + occ.ID.String() + ":" + notificationKindCreated,
		Status:         models.NotificationSkipped, MaxAttempts: 1, RetryBackoffSecs: 60, OnFailure: "manual_retry",
	}
	cfg := resolveAutomation(tx, occ.OrganizationID, occ.UnitID, occ.TeamID, automationEventForwarded)
	switch {
	case cfg == nil:
		n.Reason = "no_automation_configured"
	case contact == nil || strings.TrimSpace(contact.PhoneNumber) == "":
		n.Reason = "contact_without_phone"
	default:
		n.ConfigID = &cfg.ID
		n.WhatsAppAccount, n.TemplateName, n.TemplateLanguage = cfg.WhatsAppAccount, cfg.TemplateName, cfg.TemplateLanguage
		n.FallbackTemplateName = cfg.FallbackTemplateName
		n.MaxAttempts, n.RetryBackoffSecs, n.OnFailure = cfg.MaxAttempts, cfg.RetryBackoffSecs, cfg.OnFailure
		var acc int64
		tx.Model(&models.WhatsAppAccount{}).Where("organization_id = ? AND name = ?", occ.OrganizationID, cfg.WhatsAppAccount).Count(&acc)
		switch {
		case !cfg.IsActive:
			n.Status, n.Reason = models.NotificationPrepared, "automation_inactive"
		case acc == 0:
			n.Reason = "account_not_found_in_organization"
		case cfg.TemplateName == "" || cfg.TemplateLanguage == "":
			n.Reason = "no_template_configured"
		default:
			now := tx.NowFunc()
			n.Status, n.Reason, n.Eligible, n.NextAttemptAt = models.NotificationPending, "", true, &now
		}
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&n).Error
}

// ---------------------------------------------------------------------------
// Configuration API (accounts permission: whoever administers WhatsApp)
// ---------------------------------------------------------------------------

type automationRequest struct {
	UnitID               string  `json:"unit_id"`
	TeamID               string  `json:"team_id"`
	WhatsAppAccount      string  `json:"whatsapp_account"`
	TemplateName         string  `json:"template_name"`
	TemplateLanguage     string  `json:"template_language"`
	FallbackTemplateName string  `json:"fallback_template_name"`
	IsActive             *bool   `json:"is_active"`
	MaxAttempts          *int    `json:"max_attempts"`
	RetryBackoffSecs     *int    `json:"retry_backoff_secs"`
	OnFailure            *string `json:"on_failure"`
}

type automationResponse struct {
	models.OccurrenceChannelConfig
	UnitName     string `json:"unit_name,omitempty"`
	TeamName     string `json:"team_name,omitempty"`
	LastRunAt    any    `json:"last_run_at,omitempty"`
	LastStatus   string `json:"last_status,omitempty"`
	RecentFailed int64  `json:"recent_failures"`
}

// ListOccurrenceChannelConfigs lists the automations with their latest run and
// recent failures (last 7 days).
func (a *App) ListOccurrenceChannelConfigs(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAccounts, models.ActionRead)
	if err != nil {
		return nil
	}
	var cfgs []models.OccurrenceChannelConfig
	a.DB.Where("organization_id = ?", orgID).Order("created_at").Find(&cfgs)
	unitNames, teamNames := map[uuid.UUID]string{}, map[uuid.UUID]string{}
	var us []models.Unit
	a.DB.Select("id, name").Where("organization_id = ?", orgID).Find(&us)
	for _, u := range us {
		unitNames[u.ID] = u.Name
	}
	var ts []models.Team
	a.DB.Select("id, name").Where("organization_id = ?", orgID).Find(&ts)
	for _, t := range ts {
		teamNames[t.ID] = t.Name
	}
	out := make([]automationResponse, len(cfgs))
	for i, c := range cfgs {
		out[i] = automationResponse{OccurrenceChannelConfig: c}
		if c.UnitID != nil {
			out[i].UnitName = unitNames[*c.UnitID]
		}
		if c.TeamID != nil {
			out[i].TeamName = teamNames[*c.TeamID]
		}
		var last models.OccurrenceNotification
		if a.DB.Where("organization_id = ? AND config_id = ?", orgID, c.ID).Order("updated_at DESC").First(&last).Error == nil {
			out[i].LastRunAt, out[i].LastStatus = last.UpdatedAt, last.Status
		}
		a.DB.Model(&models.OccurrenceNotification{}).
			Where("organization_id = ? AND config_id = ? AND status IN ? AND updated_at > now() - interval '7 days'",
				orgID, c.ID, []string{models.NotificationFailed, models.NotificationUncertain}).Count(&out[i].RecentFailed)
	}
	return r.SendEnvelope(map[string]any{"configs": out})
}

// SetOccurrenceChannelConfig creates or updates the automation of one scope
// (team, else unit, else organization) and event. The account must belong to the
// organization; an active automation must name an approved template of that
// account. Existing intents keep the snapshot they were created with.
func (a *App) SetOccurrenceChannelConfig(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceAccounts, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req automationRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	cfg := models.OccurrenceChannelConfig{
		OrganizationID: orgID, Purpose: automationPurpose, Event: automationEventForwarded,
		WhatsAppAccount: strings.TrimSpace(req.WhatsAppAccount), TemplateName: strings.TrimSpace(req.TemplateName),
		TemplateLanguage: strings.TrimSpace(req.TemplateLanguage), FallbackTemplateName: strings.TrimSpace(req.FallbackTemplateName),
		IsActive: true, MaxAttempts: 3, RetryBackoffSecs: 60, OnFailure: "manual_retry",
	}
	q := a.DB.Where("organization_id = ? AND purpose = ? AND event = ?", orgID, automationPurpose, cfg.Event)

	if req.UnitID != "" {
		id, err := uuid.Parse(req.UnitID)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid unit_id", nil, "")
		}
		if _, err := findByIDAndOrg[models.Unit](a.DB, r, id, orgID, "Unit"); err != nil {
			return nil
		}
		cfg.UnitID = &id
	}
	if req.TeamID != "" {
		id, err := uuid.Parse(req.TeamID)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid team_id", nil, "")
		}
		team, err := findByIDAndOrg[models.Team](a.DB, r, id, orgID, "Team")
		if err != nil {
			return nil
		}
		if cfg.UnitID != nil && team.UnitID != nil && *team.UnitID != *cfg.UnitID {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "team_id does not belong to unit_id", nil, "")
		}
		if cfg.UnitID == nil {
			cfg.UnitID = team.UnitID
		}
		cfg.TeamID = &id
	}
	switch {
	case cfg.TeamID != nil:
		q = q.Where("team_id = ?", *cfg.TeamID)
	case cfg.UnitID != nil:
		q = q.Where("unit_id = ? AND team_id IS NULL", *cfg.UnitID)
	default:
		q = q.Where("unit_id IS NULL AND team_id IS NULL")
	}
	var existing models.OccurrenceChannelConfig
	exists := q.First(&existing).Error == nil
	if exists {
		cfg.ID, cfg.CreatedAt = existing.ID, existing.CreatedAt
		cfg.IsActive, cfg.MaxAttempts, cfg.RetryBackoffSecs, cfg.OnFailure = existing.IsActive, existing.MaxAttempts, existing.RetryBackoffSecs, existing.OnFailure
	}
	if req.IsActive != nil {
		cfg.IsActive = *req.IsActive
	}
	if req.MaxAttempts != nil {
		if *req.MaxAttempts < 1 || *req.MaxAttempts > 10 {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "max_attempts must be between 1 and 10", nil, "")
		}
		cfg.MaxAttempts = *req.MaxAttempts
	}
	if req.RetryBackoffSecs != nil {
		if *req.RetryBackoffSecs < 10 || *req.RetryBackoffSecs > 86400 {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "retry_backoff_secs must be between 10 and 86400", nil, "")
		}
		cfg.RetryBackoffSecs = *req.RetryBackoffSecs
	}
	if req.OnFailure != nil {
		if *req.OnFailure != "manual_retry" && *req.OnFailure != "cancel" {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "on_failure must be manual_retry or cancel", nil, "")
		}
		cfg.OnFailure = *req.OnFailure
	}

	// The account must be this organization's. Nothing falls back to another one.
	var accN int64
	a.DB.Model(&models.WhatsAppAccount{}).Where("organization_id = ? AND name = ?", orgID, cfg.WhatsAppAccount).Count(&accN)
	if accN == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Unknown WhatsApp account", nil, "")
	}
	if cfg.IsActive {
		if msg := a.checkAutomationTemplate(orgID, cfg.WhatsAppAccount, cfg.TemplateName, cfg.TemplateLanguage); msg != "" {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
		}
		if cfg.FallbackTemplateName != "" {
			if msg := a.checkAutomationTemplate(orgID, cfg.WhatsAppAccount, cfg.FallbackTemplateName, cfg.TemplateLanguage); msg != "" {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "fallback: "+msg, nil, "")
			}
		}
	}

	var saveErr error
	if exists {
		saveErr = a.DB.Select("*").Omit("created_at").Save(&cfg).Error
	} else {
		saveErr = a.DB.Select("*").Create(&cfg).Error
	}
	if saveErr != nil {
		a.Log.Error("Failed to save automation", "error", saveErr)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save automation", nil, "")
	}
	a.logAudit(orgID, userID, "occurrence_automation", cfg.ID, models.AuditActionUpdated, existing, cfg)
	return r.SendEnvelope(cfg)
}

// checkAutomationTemplate returns why a template cannot back an automation, or
// "" when it can. The provider's own data decides: the template must exist on
// that account, be APPROVED and be UTILITY. Nothing is assumed from the text.
func (a *App) checkAutomationTemplate(orgID uuid.UUID, account, name, language string) string {
	if name == "" || language == "" {
		return "template_name and template_language are required for an active automation"
	}
	var t models.Template
	if err := a.DB.Where("organization_id = ? AND whats_app_account = ? AND name = ? AND language = ?", orgID, account, name, language).First(&t).Error; err != nil {
		return "Template not found on this account and language"
	}
	if !strings.EqualFold(t.Status, "APPROVED") {
		return "Template is not approved (status " + t.Status + ")"
	}
	if !strings.EqualFold(t.Category, "UTILITY") {
		return "Template category is " + t.Category + "; only UTILITY templates are allowed for automatic messages"
	}
	return ""
}

// DeleteOccurrenceChannelConfig removes an automation. Intents already created
// keep their own snapshot and are not touched.
func (a *App) DeleteOccurrenceChannelConfig(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceAccounts, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "automation")
	if err != nil {
		return nil
	}
	cfg, err := findByIDAndOrg[models.OccurrenceChannelConfig](a.DB, r, id, orgID, "Automation")
	if err != nil {
		return nil
	}
	if err := a.DB.Delete(cfg).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete automation", nil, "")
	}
	a.logAudit(orgID, userID, "occurrence_automation", cfg.ID, models.AuditActionDeleted, cfg, nil)
	return r.SendEnvelope(map[string]bool{"deleted": true})
}
