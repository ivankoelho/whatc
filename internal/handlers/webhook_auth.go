package handlers

import (
	"crypto/subtle"

	"github.com/shridarpatil/whatomate/internal/models"
)

// authenticateWebhook decides whether a POST /api/webhook really comes from Meta.
//
// Meta signs the RAW body with HMAC-SHA256 using the App Secret and sends the
// result in X-Hub-Signature-256. This endpoint changes message, call, template
// and contact state, so it fails closed:
//
//   - a missing, malformed or wrong signature is rejected;
//   - every account the payload refers to (by phone_number_id, or by WABA id for
//     template updates) must have a secret, and the signature must verify under
//     EACH of them. Verifying under just one would let the owner of one account
//     (who knows its secret) forge events for another organization's account;
//   - the secret is the account's own App Secret, else the instance-wide
//     whatsapp.app_secret. With neither, the payload is rejected, never accepted;
//   - a payload that names no account cannot be attributed, so it is rejected.
//
// There is deliberately no switch to disable this. Local testing signs its
// payloads with the account's secret (test/tools/mock-meta.js can do it).
//
// It returns "" when authentic, otherwise a short reason for the log. The reason
// never contains a secret or any of the body.
func (a *App) authenticateWebhook(body, signature []byte, payload *WebhookPayload) string {
	// A body with no change in it asks for nothing: there is no state to protect.
	hasChange := false
	for _, entry := range payload.Entry {
		if len(entry.Changes) > 0 {
			hasChange = true
		}
	}
	if !hasChange {
		return ""
	}
	if len(signature) == 0 {
		return "missing_signature"
	}

	phoneIDs := map[string]bool{}
	wabaIDs := map[string]bool{}
	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			if id := change.Value.Metadata.PhoneNumberID; id != "" {
				phoneIDs[id] = true
			} else if entry.ID != "" {
				wabaIDs[entry.ID] = true
			}
		}
	}
	if len(phoneIDs) == 0 && len(wabaIDs) == 0 {
		return "no_account_referenced" // it asks for something, but names no one to vouch for it
	}

	var accounts []models.WhatsAppAccount
	for id := range phoneIDs {
		acc, err := a.getWhatsAppAccountCached(id)
		if err != nil || acc == nil {
			return "unknown_phone_number_id"
		}
		accounts = append(accounts, *acc)
	}
	for id := range wabaIDs {
		var found []models.WhatsAppAccount
		if err := a.DB.Where("business_id = ?", id).Find(&found).Error; err != nil || len(found) == 0 {
			return "unknown_waba_id"
		}
		for i := range found {
			a.decryptAccountSecrets(&found[i])
		}
		accounts = append(accounts, found...)
	}

	for i := range accounts {
		secret := accounts[i].AppSecret
		if secret == "" {
			secret = a.Config.WhatsApp.AppSecret
		}
		if secret == "" {
			return "no_secret_configured"
		}
		if !verifyWebhookSignature(body, signature, []byte(secret)) {
			return "invalid_signature"
		}
	}
	return ""
}

// equalConstTime compares two secrets/tokens without leaking where they differ.
func equalConstTime(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
