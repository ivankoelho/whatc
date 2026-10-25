package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shridarpatil/whatomate/internal/contactutil"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// Finding a contact is not reading its conversation.
//
// The regular contact list is scoped by who may see each conversation, which
// is right for the chat but leaves a SAC or Logistics agent unable to find a
// customer whose chat another agent holds, even to ask for a case about them.
// LookupContacts answers only "is this customer in my organization, and what is
// their number and name", and nothing else: no cadastral data (CPF/CNPJ), no
// owner, no messages, no unit placement. Opening the conversation, replying and
// creating cases keep their own rules.
//
// Anti-enumeration: a minimum query size, at most 10 results, no paging, and a
// per-user rate limit. The organization comes from the session, never the request.
const (
	lookupMinDigits   = 8
	lookupMinChars    = 4
	lookupMaxResults  = 10
	lookupPerMinute   = 30
	lookupRateKeyBase = "contact_lookup:"
)

type contactLookupResult struct {
	ID          string `json:"id"`
	PhoneNumber string `json:"phone_number"`
	ProfileName string `json:"profile_name"`
	// CanOpenConversation tells the UI whether a link to the chat would work for
	// this caller; it reveals nothing about who holds the conversation.
	CanOpenConversation bool `json:"can_open_conversation"`
}

func (a *App) LookupContacts(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceContacts, models.ActionRead)
	if err != nil {
		return nil
	}
	q := strings.TrimSpace(string(r.RequestCtx.QueryArgs().Peek("q")))
	digits := contactutil.NormalizePhone(q)
	numeric := len(digits) > 0 && len(digits) >= len(q)-3 // looks like a phone, not a name
	byPhone := numeric && len(digits) >= lookupMinDigits
	if (numeric && !byPhone) || (!numeric && len([]rune(q)) < lookupMinChars) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			fmt.Sprintf("Type at least %d digits of the phone or %d letters of the name", lookupMinDigits, lookupMinChars), nil, "")
	}
	if len(q) > 100 {
		q = q[:100]
	}

	if a.Redis != nil {
		key := lookupRateKeyBase + userID.String()
		n, err := a.Redis.Incr(context.Background(), key).Result()
		if err == nil {
			if n == 1 {
				a.Redis.Expire(context.Background(), key, time.Minute)
			}
			if n > lookupPerMinute {
				return r.SendErrorEnvelope(fasthttp.StatusTooManyRequests, "Too many lookups; wait a moment", nil, "")
			}
		}
	}

	query := a.DB.Model(&models.Contact{}).Where("organization_id = ?", orgID)
	if byPhone {
		query = query.Where("phone_number LIKE ?", "%"+digits+"%")
	} else {
		query = query.Where("profile_name ILIKE ?", "%"+escapeLike(q)+"%")
	}
	var contacts []models.Contact
	if err := query.Select("id, organization_id, phone_number, profile_name, assigned_user_id, team_id, whats_app_account").
		Order("profile_name, phone_number").Limit(lookupMaxResults).Find(&contacts).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Lookup failed", nil, "")
	}
	out := make([]contactLookupResult, len(contacts))
	for i := range contacts {
		c := &contacts[i]
		out[i] = contactLookupResult{ID: c.ID.String(), PhoneNumber: c.PhoneNumber, ProfileName: c.ProfileName,
			CanOpenConversation: a.canViewConversation(userID, orgID, c)}
	}
	return r.SendEnvelope(map[string]any{"contacts": out})
}

// escapeLike keeps % and _ typed by the user literal.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
