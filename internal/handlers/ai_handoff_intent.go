package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/models"
)

// Durable handoff (P1-01).
//
// The agent's last words ("Perfeito, vou chamar um vendedor…") promise a person. Before that text is
// handed to WhatsApp, the promise is written to ai_handoff_intents. If the process dies after the text
// left and before the attendance exists, the intent is still there: recoverAIHandoffs (run by the same
// worker loop as the wait, on every instance) finds it once its lease has passed and finishes the
// handoff. Finishing is idempotent: one active attendance per contact is enforced by the database, so a
// second run finds it (already_active) and sends nothing.
//
// Order of events in the live path, and what each crash point leaves behind:
//
//	1. intent recorded                      crash here: nothing was promised, nothing to recover
//	2. farewell sent to the customer        crash here: recovery completes the handoff (the P1-01 case)
//	3. qualification / CRM / transfer       crash here: recovery re-runs it (no second extraction once one
//	                                        succeeded, no second opportunity, no second attendance)
//	4. waiting message                      crash here: the attendance exists, recovery sees it and ends
//	5. intent marked done
//
// If the intent cannot be recorded, the farewell is NOT sent: the handoff is made first and the customer
// is told only afterwards (the order every other handoff reason already has).

const (
	// aiHandoffLease is how long a run is trusted to be alive before another may take its intent over. It
	// covers the longest honest handoff: the qualification call (aiCallDeadline) and the transfer.
	aiHandoffLease = 3 * time.Minute
	// aiHandoffMaxAttempts: runs tried before the intent is abandoned for a person to look at.
	aiHandoffMaxAttempts = 3
)

// aiHandoffFault is a test seam: a function that may panic to simulate the process dying at a named
// point. It is nil in production, where aiHandoffFaultPoint does nothing.
var aiHandoffFault func(point string)

func aiHandoffFaultPoint(point string) {
	if aiHandoffFault != nil {
		aiHandoffFault(point)
	}
}

// recordHandoffIntent writes the promise before it is made. It is idempotent: a contact that already has
// a pending intent keeps it (its lease is extended), so a repeated or concurrent turn never records two.
func (a *App) recordHandoffIntent(account *models.WhatsAppAccount, contact *models.Contact, session *models.ChatbotSession, reason string) (uuid.UUID, error) {
	now := time.Now()
	lease := now.Add(aiHandoffLease)
	in := models.AIHandoffIntent{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  account.OrganizationID,
		ContactID:       contact.ID,
		SessionID:       session.ID,
		WhatsAppAccount: account.Name,
		Reason:          reason,
		Status:          models.AIHandoffIntentPending,
		LockedUntil:     &lease,
	}
	err := a.DB.Create(&in).Error
	if err == nil {
		a.Log.Info("AI handoff intent recorded", "intent_id", in.ID, "contact_id", contact.ID, "reason", reason)
		return in.ID, nil
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23505" {
		return uuid.Nil, err
	}
	// one is already pending for this contact: it is the same promise
	var existing models.AIHandoffIntent
	if err := a.DB.Where("contact_id = ? AND status = ?", contact.ID, models.AIHandoffIntentPending).First(&existing).Error; err != nil {
		return uuid.Nil, err
	}
	a.DB.Model(&models.AIHandoffIntent{}).Where("id = ?", existing.ID).Update("locked_until", lease)
	return existing.ID, nil
}

// finishHandoffIntent closes a pending intent with how it ended. Closing twice changes nothing.
func (a *App) finishHandoffIntent(id uuid.UUID, outcome string, recovered bool) {
	if id == uuid.Nil {
		return
	}
	now := time.Now()
	updates := map[string]any{"status": models.AIHandoffIntentDone, "outcome": outcome, "finished_at": now, "locked_until": nil}
	if recovered {
		updates["recovered_at"] = now
	}
	if err := a.DB.Model(&models.AIHandoffIntent{}).Where("id = ? AND status = ?", id, models.AIHandoffIntentPending).Updates(updates).Error; err != nil {
		// not fatal: the intent stays pending and recovery will find the attendance already there
		a.Log.Error("Failed to close an AI handoff intent", "error", err, "intent_id", id)
	}
}

// recoverAIHandoffs completes the handoffs whose run is gone. A run is gone when its lease has passed.
// Several instances may call it at once: each claim names the lease it saw, so only one wins a row.
func (a *App) recoverAIHandoffs(now time.Time) int {
	var due []models.AIHandoffIntent
	if err := a.DB.Where("status = ? AND locked_until < ?", models.AIHandoffIntentPending, now).
		Order("created_at").Limit(20).Find(&due).Error; err != nil {
		a.Log.Error("Failed to look for AI handoffs to recover", "error", err)
		return 0
	}
	n := 0
	for i := range due {
		in := due[i]
		if in.Attempts >= aiHandoffMaxAttempts {
			a.abandonHandoffIntent(&in, now)
			continue
		}
		// claim: only if nobody renewed or took it since we looked
		res := a.DB.Model(&models.AIHandoffIntent{}).
			Where("id = ? AND status = ? AND attempts = ? AND locked_until = ?", in.ID, models.AIHandoffIntentPending, in.Attempts, *in.LockedUntil).
			Updates(map[string]any{"attempts": in.Attempts + 1, "locked_until": now.Add(aiHandoffLease)})
		if res.Error != nil || res.RowsAffected == 0 {
			continue
		}
		in.Attempts++
		a.runHandoffRecovery(&in)
		n++
	}
	return n
}

// runHandoffRecovery carries one claimed intent out. A panic or a failure leaves it pending: the lease
// expires and a later run tries again, until aiHandoffMaxAttempts.
func (a *App) runHandoffRecovery(in *models.AIHandoffIntent) {
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("AI handoff recovery panicked", "intent_id", in.ID, "panic", r)
			a.DB.Model(&models.AIHandoffIntent{}).Where("id = ?", in.ID).Update("last_error", "panic")
		}
	}()
	var account models.WhatsAppAccount
	if err := a.DB.Where("organization_id = ? AND name = ?", in.OrganizationID, in.WhatsAppAccount).First(&account).Error; err != nil {
		a.finishHandoffIntent(in.ID, "gone", true)
		return
	}
	a.decryptAccountSecrets(&account) // loaded straight from the database: the token is still the stored cipher
	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", in.ContactID, in.OrganizationID).First(&contact).Error; err != nil {
		a.finishHandoffIntent(in.ID, "gone", true)
		return
	}
	var session models.ChatbotSession
	if err := a.DB.Where("id = ?", in.SessionID).First(&session).Error; err != nil {
		a.finishHandoffIntent(in.ID, "gone", true)
		return
	}
	settings, err := a.getChatbotSettingsCached(in.OrganizationID, account.Name)
	if err != nil || settings == nil {
		a.DB.Model(&models.AIHandoffIntent{}).Where("id = ?", in.ID).Update("last_error", "settings_unavailable")
		return // pending: retried
	}
	outcome := a.handoffFromAIOutcome(&account, &contact, &session, settings, in.Reason)
	a.Log.Info("AI handoff recovered", "intent_id", in.ID, "contact_id", in.ContactID, "outcome", outcome, "attempts", in.Attempts)
	a.finishHandoffIntent(in.ID, outcome, true)
}

// abandonHandoffIntent gives up on an intent that failed every attempt. It is loud on purpose: the
// customer was promised a person and none was assigned.
func (a *App) abandonHandoffIntent(in *models.AIHandoffIntent, now time.Time) {
	res := a.DB.Model(&models.AIHandoffIntent{}).Where("id = ? AND status = ?", in.ID, models.AIHandoffIntentPending).
		Updates(map[string]any{"status": models.AIHandoffIntentAbandoned, "outcome": "abandoned", "finished_at": now, "locked_until": nil})
	if res.Error == nil && res.RowsAffected > 0 {
		a.Log.Error("AI handoff abandoned: the customer was promised a person and none was assigned",
			"intent_id", in.ID, "contact_id", in.ContactID, "org_id", in.OrganizationID)
	}
}
