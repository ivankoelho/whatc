package handlers

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/shridarpatil/whatomate/internal/models"
)

// Button ids of a confirmation message. The token is random and single use; it is the only thing a
// button carries, never an action.
const (
	aiToolConfirmPrefix = "aitc:"
	aiToolDeclinePrefix = "aitd:"
)

// outOfHoursFallback is what the customer hears when a confirmed request meets a closed organization
// that has no out-of-hours message of its own.
const outOfHoursFallback = "No momento estamos fora do horário de atendimento. Tente novamente no nosso horário de funcionamento."

// aiToolConfirmationsReady says whether the customer-confirmation mechanism can run: it needs the
// server secret that binds each confirmation to its action.
func (a *App) aiToolConfirmationsReady() bool {
	return a.Config != nil && a.Config.App.EncryptionKey != ""
}

func (a *App) aiToolConfirmationStore() *aitools.ConfirmationStore {
	cfg := a.Config.AITools
	return &aitools.ConfirmationStore{
		DB: a.DB, Secret: a.Config.App.EncryptionKey,
		Params: aitools.ConfirmationParams{
			TTL: cfg.ConfirmationTTL(), Limit: cfg.ProposalLimitPerWindow(), Window: cfg.ProposalWindow(), Cooldown: cfg.DeclineCooldown(),
		},
	}
}

// --- adapters between the application and the aitools ports ---

// appTransferService is the application's transfer logic behind aitools.TransferService.
type appTransferService struct{ a *App }

func (s appTransferService) HasActive(_ context.Context, orgID, contactID uuid.UUID) bool {
	return s.a.hasActiveAgentTransfer(orgID, contactID)
}

func (s appTransferService) WithinBusinessHours(_ context.Context, orgID uuid.UUID, account string) bool {
	settings, _ := s.a.getChatbotSettingsCached(orgID, account)
	if settings != nil && settings.BusinessHours.Enabled && len(settings.BusinessHours.Hours) > 0 {
		return s.a.isWithinBusinessHours(settings.BusinessHours.Hours)
	}
	return true
}

func (s appTransferService) FindAITransfer(_ context.Context, orgID, contactID uuid.UUID, since time.Time) (uuid.UUID, bool) {
	var t models.AgentTransfer
	err := s.a.DB.Where("organization_id = ? AND contact_id = ? AND source = ? AND transferred_at >= ?",
		orgID, contactID, models.TransferSourceAIConfirmed, since).Order("transferred_at ASC").First(&t).Error
	if err != nil {
		return uuid.Nil, false
	}
	return t.ID, true
}

func (s appTransferService) TransferToQueue(ctx context.Context, sc aitools.Scope, notes string) (aitools.ExecResult, error) {
	account, err := s.a.resolveWhatsAppAccount(sc.OrganizationID, sc.WhatsAppAccount)
	if err != nil {
		return aitools.ExecResult{}, err
	}
	var contact models.Contact
	if err := s.a.DB.Where("id = ? AND organization_id = ?", *sc.ContactID, sc.OrganizationID).First(&contact).Error; err != nil {
		return aitools.ExecResult{}, err
	}
	res, err := s.a.TransferToQueue(ctx, account, &contact, models.TransferSourceAIConfirmed, notes)
	if err != nil {
		return aitools.ExecResult{}, err
	}
	switch res.Outcome {
	case TransferCreated:
		id := res.TransferID
		return aitools.ExecResult{Outcome: models.AIConfirmationOutcomeCreated, TransferID: &id}, nil
	case TransferOutsideHours:
		return aitools.ExecResult{Outcome: models.AIConfirmationOutcomeOutsideHours}, nil
	}
	return aitools.ExecResult{Outcome: models.AIConfirmationOutcomeAlreadyActive}, nil
}

// appConfirmerEnv is the live state the re-authorization reads.
type appConfirmerEnv struct{ a *App }

func (e appConfirmerEnv) GlobalEnabled() bool { return e.a.aiToolsGloballyEnabled() }
func (e appConfirmerEnv) WriteEnabled() bool {
	return e.a.Config != nil && e.a.Config.AITools.WriteEnabled
}

func (e appConfirmerEnv) ProviderValidated(_ context.Context, orgID uuid.UUID) bool {
	var providers []string
	if e.a.DB.Model(&models.ChatbotSettings{}).Where("organization_id = ? AND whats_app_account = ''", orgID).
		Limit(1).Pluck("ai_provider", &providers).Error != nil || len(providers) == 0 {
		return false
	}
	return e.a.Config.AITools.ProviderValidated(providers[0])
}

func (e appConfirmerEnv) ChatbotEnabled(_ context.Context, orgID uuid.UUID, account string) bool {
	settings, err := e.a.getChatbotSettingsCached(orgID, account)
	return err == nil && settings != nil && settings.IsEnabled
}

// aiToolConfirmer builds the Confirmer with the current configuration.
func (a *App) aiToolConfirmer() *aitools.Confirmer {
	cfg := a.Config.AITools
	return &aitools.Confirmer{
		Store: a.aiToolConfirmationStore(), Catalog: a.aiToolCatalog(), Enabled: aitools.SettingsStore{DB: a.DB},
		Auditor: aitools.DBAuditor{DB: a.DB, Secret: a.Config.App.EncryptionKey},
		Deps:    aitools.WriteDeps{Transfers: appTransferService{a: a}}, Env: appConfirmerEnv{a: a},
		ReconcileMinAge: cfg.ReconcileMinAge(), ReconcileMaxAttempts: cfg.ReconcileAttempts(), Log: a.Log,
		Notify: a.notifyAIToolCustomer,
	}
}

// notifyAIToolCustomer sends the customer a message the reconciler produced.
func (a *App) notifyAIToolCustomer(_ context.Context, n aitools.Notification) {
	account, err := a.resolveWhatsAppAccount(n.OrgID, n.Account)
	if err != nil {
		a.Log.Error("AI tools: could not notify the customer", "error", err, "organization_id", n.OrgID.String())
		return
	}
	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", n.ContactID, n.OrgID).First(&contact).Error; err != nil {
		return
	}
	text := n.Text
	if n.OutsideHours {
		text = a.outOfHoursText(account)
	}
	if text != "" {
		_ = a.sendAndSaveTextMessage(account, &contact, text)
	}
}

func (a *App) outOfHoursText(account *models.WhatsAppAccount) string {
	if settings, _ := a.getChatbotSettingsCached(account.OrganizationID, account.Name); settings != nil && settings.BusinessHours.OutOfHoursMessage != "" {
		return settings.BusinessHours.OutOfHoursMessage
	}
	return outOfHoursFallback
}

// isAIToolConfirmationButton says whether a button id belongs to an AI confirmation message.
func isAIToolConfirmationButton(id string) bool {
	return strings.HasPrefix(id, aiToolConfirmPrefix) || strings.HasPrefix(id, aiToolDeclinePrefix)
}

// handleAIToolConfirmationTap processes the customer's tap on a confirmation button. The webhook
// already identified the contact (the verified sender). Whatever the result, the tap is consumed here:
// it never reaches the chatbot as a normal message.
func (a *App) handleAIToolConfirmationTap(account *models.WhatsAppAccount, contact *models.Contact, buttonID, wamid string) {
	if !a.aiToolConfirmationsReady() {
		return
	}
	decline := strings.HasPrefix(buttonID, aiToolDeclinePrefix)
	token := strings.TrimPrefix(strings.TrimPrefix(buttonID, aiToolConfirmPrefix), aiToolDeclinePrefix)
	// the sender's current session: the latest active one of this contact on this account. Read only.
	var session models.ChatbotSession
	if err := a.DB.Where("organization_id = ? AND contact_id = ? AND whats_app_account = ? AND status = ?",
		account.OrganizationID, contact.ID, account.Name, models.SessionStatusActive).
		Order("started_at DESC").First(&session).Error; err != nil {
		return // no current session: the button is not applicable
	}
	res := a.aiToolConfirmer().HandleTap(context.Background(), aitools.Tap{
		Token: token, OrgID: account.OrganizationID, ContactID: contact.ID, SessionID: session.ID, Account: account.Name, WAMID: wamid, Decline: decline,
	})
	if res.Kind != aitools.TapReply {
		return // unknown, repeated or inert: nothing to say
	}
	text := res.Text
	if res.OutsideHours {
		text = a.outOfHoursText(account)
	}
	if text != "" {
		if err := a.sendAndSaveTextMessage(account, contact, text); err != nil {
			a.Log.Error("AI tools: failed to answer a confirmation tap", "error", err, "contact", contact.PhoneNumber)
		}
	}
}

// aiConfirmationButtons is the server-composed confirmation message of a proposal: the text and the
// two buttons. Nothing of it comes from the model.
func aiConfirmationButtons(p *aitools.PendingConfirmation) (string, []map[string]any) {
	return p.Spec.Prompt(p.TTL), []map[string]any{
		{"id": aiToolConfirmPrefix + p.Token, "title": p.Spec.ConfirmLabel, "type": "reply"},
		{"id": aiToolDeclinePrefix + p.Token, "title": p.Spec.DeclineLabel, "type": "reply"},
	}
}
