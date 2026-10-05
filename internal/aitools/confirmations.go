package aitools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// The confirmation store keeps the customer's confirmations of what the AI proposed (Fase 9D). Every
// change of state is a compare-and-set from the state it comes from, so two processes can never both
// win: a confirmation is consumed once. After pending -> confirmed the token authorizes nothing more;
// whatever is left unfinished is closed by the reconciler, never by the button.

const confirmPurpose = "whatc/ai_tools/confirm/v1"

// ConfirmationParams are the operational numbers, from configuration.
type ConfirmationParams struct {
	TTL      time.Duration // how long a proposal waits for the customer
	Limit    int           // proposals per contact...
	Window   time.Duration // ...in this window
	Cooldown time.Duration // pause after a refusal (0 = none)
}

var (
	// ErrTooManyProposals: the contact already got Limit proposals in the window.
	ErrTooManyProposals = errors.New("too many proposals")
	// ErrRecentlyDeclined: the customer refused less than Cooldown ago.
	ErrRecentlyDeclined = errors.New("recently declined")
	// ErrNoSecret: there is no server secret to bind confirmations with; nothing can be proposed.
	ErrNoSecret = errors.New("no secret configured for confirmations")
)

// ConfirmationStore reads and writes ai_tool_confirmations.
type ConfirmationStore struct {
	DB     *gorm.DB
	Secret string // the server secret; the digest key is derived from it
	Params ConfirmationParams
	// Now is the clock; time.Now when nil. Tests inject a fixed one.
	Now func() time.Time
}

func (s *ConfirmationStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// HashToken is the SHA-256 (hex) of a token; only the hash is ever stored.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newToken makes a 128-bit random token, base64url (22 characters).
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ActionDigest binds a confirmation to exactly one action: an HMAC over the canonical form of the
// organization, the contact, the tool and its (sanitized) arguments. If any of them is altered in the
// database the digest no longer matches and the confirmation is refused.
func ActionDigest(secret string, orgID, contactID uuid.UUID, tool string, args models.JSONB) (string, error) {
	if secret == "" {
		return "", ErrNoSecret
	}
	raw, err := json.Marshal(map[string]any{"o": orgID.String(), "c": contactID.String(), "t": tool, "a": args})
	if err != nil {
		return "", err
	}
	canon, err := CanonicalJSON(raw)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(mac(derivedKeyFor(secret, confirmPurpose), canon)), nil
}

// ProposeInput is what the proposing tool call knows.
type ProposeInput struct {
	Scope          Scope // OrganizationID, ContactID and SessionID are required
	RunID          uuid.UUID
	Tool           string
	Args           models.JSONB // canonical, sanitized
	ProposalCallID *uuid.UUID
}

// Propose records a new pending confirmation and returns it with the token to put in the buttons.
// It refuses (ErrTooManyProposals, ErrRecentlyDeclined) when the contact is over the limit or in the
// pause after a refusal, and supersedes any older pending proposal of the same contact and tool.
// The whole decision runs under a per-contact lock, so concurrent proposals cannot exceed the limit.
func (s *ConfirmationStore) Propose(ctx context.Context, in ProposeInput) (token string, conf models.AIToolConfirmation, err error) {
	sc := in.Scope
	if sc.OrganizationID == uuid.Nil || sc.ContactID == nil || sc.SessionID == nil {
		return "", conf, errors.New("aitools: a proposal needs organization, contact and session")
	}
	digest, err := ActionDigest(s.Secret, sc.OrganizationID, *sc.ContactID, in.Tool, in.Args)
	if err != nil {
		return "", conf, err
	}
	token, err = newToken()
	if err != nil {
		return "", conf, err
	}
	now := s.now()
	conf = models.AIToolConfirmation{
		OrganizationID: sc.OrganizationID, ContactID: *sc.ContactID, SessionID: *sc.SessionID, RunID: in.RunID,
		ToolName: in.Tool, Risk: string(RiskWrite), TokenHash: HashToken(token), Args: in.Args, ActionDigest: digest,
		Status: models.AIConfirmationPending, ProposedAt: now, ExpiresAt: now.Add(s.Params.TTL), ProposalCallID: in.ProposalCallID,
	}

	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lockKey := sc.OrganizationID.String() + "/" + sc.ContactID.String()
		if e := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", lockKey).Error; e != nil {
			return e
		}
		if s.Params.Cooldown > 0 {
			var declined int64
			if e := tx.Model(&models.AIToolConfirmation{}).
				Where("organization_id = ? AND contact_id = ? AND tool_name = ? AND status = ? AND finished_at >= ?",
					sc.OrganizationID, *sc.ContactID, in.Tool, models.AIConfirmationDeclined, now.Add(-s.Params.Cooldown)).
				Count(&declined).Error; e != nil {
				return e
			}
			if declined > 0 {
				return ErrRecentlyDeclined
			}
		}
		var recent int64
		if e := tx.Model(&models.AIToolConfirmation{}).
			Where("organization_id = ? AND contact_id = ? AND tool_name = ? AND proposed_at >= ?",
				sc.OrganizationID, *sc.ContactID, in.Tool, now.Add(-s.Params.Window)).
			Count(&recent).Error; e != nil {
			return e
		}
		if recent >= int64(s.Params.Limit) {
			return ErrTooManyProposals
		}
		if e := tx.Model(&models.AIToolConfirmation{}).
			Where("organization_id = ? AND contact_id = ? AND tool_name = ? AND status = ?",
				sc.OrganizationID, *sc.ContactID, in.Tool, models.AIConfirmationPending).
			Updates(map[string]any{"status": models.AIConfirmationSuperseded, "finished_at": now}).Error; e != nil {
			return e
		}
		return tx.Create(&conf).Error
	})
	if err != nil {
		return "", models.AIToolConfirmation{}, err
	}
	return token, conf, nil
}

// LookupResult says what Lookup found.
type LookupResult int

const (
	LookupOK         LookupResult = iota
	LookupNotFound                // no such token, or it is not this organization's, contact's or session's
	LookupNotPending              // it exists and is theirs but is not pending: the button is inert
	LookupExpired                 // pending but past its validity
	LookupTampered                // the stored action no longer matches its digest
)

// Lookup finds the confirmation of a button token, for the contact who tapped it. A token that
// belongs to another organization, another contact or another session than the sender's current one
// looks exactly like an unknown token. These checks do not consume anything.
func (s *ConfirmationStore) Lookup(ctx context.Context, token string, orgID, contactID, sessionID uuid.UUID) (*models.AIToolConfirmation, LookupResult) {
	if token == "" || orgID == uuid.Nil || contactID == uuid.Nil || sessionID == uuid.Nil {
		return nil, LookupNotFound
	}
	var c models.AIToolConfirmation
	err := s.DB.WithContext(ctx).Where("token_hash = ? AND organization_id = ? AND contact_id = ?", HashToken(token), orgID, contactID).First(&c).Error
	if err != nil {
		return nil, LookupNotFound
	}
	if c.SessionID != sessionID {
		return nil, LookupNotFound
	}
	var sessions int64
	s.DB.WithContext(ctx).Table("chatbot_sessions").
		Where("id = ? AND organization_id = ? AND contact_id = ?", c.SessionID, orgID, contactID).Count(&sessions)
	if sessions == 0 {
		return nil, LookupNotFound
	}
	if c.Status != models.AIConfirmationPending {
		return &c, LookupNotPending
	}
	if !s.now().Before(c.ExpiresAt) {
		return &c, LookupExpired
	}
	want, err := ActionDigest(s.Secret, c.OrganizationID, c.ContactID, c.ToolName, c.Args)
	if err != nil || want != c.ActionDigest {
		return &c, LookupTampered
	}
	return &c, LookupOK
}

// transition is the one way a state changes: UPDATE ... WHERE status IN from. It reports whether
// THIS call made the change.
func (s *ConfirmationStore) transition(ctx context.Context, id uuid.UUID, from []string, set map[string]any) (bool, error) {
	res := s.DB.WithContext(ctx).Model(&models.AIToolConfirmation{}).Where("id = ? AND status IN ?", id, from).Updates(set)
	return res.RowsAffected == 1, res.Error
}

// Decline: pending -> declined.
func (s *ConfirmationStore) Decline(ctx context.Context, id uuid.UUID) (bool, error) {
	return s.transition(ctx, id, []string{models.AIConfirmationPending},
		map[string]any{"status": models.AIConfirmationDeclined, "finished_at": s.now()})
}

// Expire: pending -> expired.
func (s *ConfirmationStore) Expire(ctx context.Context, id uuid.UUID) (bool, error) {
	return s.transition(ctx, id, []string{models.AIConfirmationPending},
		map[string]any{"status": models.AIConfirmationExpired, "finished_at": s.now()})
}

// Deny: pending -> denied, with the reason re-authorization gave. It never passes through confirmed.
func (s *ConfirmationStore) Deny(ctx context.Context, id uuid.UUID, reason string) (bool, error) {
	return s.transition(ctx, id, []string{models.AIConfirmationPending},
		map[string]any{"status": models.AIConfirmationDenied, "denial_reason": reason, "finished_at": s.now()})
}

// CloseNotExecuted: pending -> not_executed with an outcome (already_active, outside_hours), for what
// the checks before the CAS already show. The customer's authorization was not consumed.
func (s *ConfirmationStore) CloseNotExecuted(ctx context.Context, id uuid.UUID, outcome string) (bool, error) {
	return s.transition(ctx, id, []string{models.AIConfirmationPending},
		map[string]any{"status": models.AIConfirmationNotExecuted, "outcome": outcome, "finished_at": s.now()})
}

// Confirm is THE compare-and-set: pending -> confirmed, recording who confirmed and with which
// message. Only the caller that gets true may go on to execute.
func (s *ConfirmationStore) Confirm(ctx context.Context, id, byContact uuid.UUID, wamid string) (bool, error) {
	return s.transition(ctx, id, []string{models.AIConfirmationPending}, map[string]any{
		"status": models.AIConfirmationConfirmed, "confirmed_at": s.now(), "confirmed_by_contact_id": byContact, "confirm_wamid": wamid,
	})
}

// FinishInput is the outcome of a confirmed action.
type FinishInput struct {
	Status          string // executed | not_executed | failed
	Outcome         string
	TransferID      *uuid.UUID
	ErrorKind       string
	DenialReason    string
	ExecutionCallID *uuid.UUID
}

// Finish: confirmed -> executed | not_executed | failed | denied.
func (s *ConfirmationStore) Finish(ctx context.Context, id uuid.UUID, in FinishInput) (bool, error) {
	switch in.Status {
	case models.AIConfirmationExecuted, models.AIConfirmationNotExecuted, models.AIConfirmationFailed, models.AIConfirmationDenied:
	default:
		return false, fmt.Errorf("aitools: %q is not an outcome of a confirmed action", in.Status)
	}
	set := map[string]any{"status": in.Status, "outcome": in.Outcome, "error_kind": in.ErrorKind, "denial_reason": in.DenialReason, "finished_at": s.now()}
	if in.TransferID != nil {
		set["transfer_id"] = *in.TransferID
	}
	if in.ExecutionCallID != nil {
		set["execution_call_id"] = *in.ExecutionCallID
	}
	return s.transition(ctx, id, []string{models.AIConfirmationConfirmed}, set)
}

// SetExecutionCall links the audit row of the confirmed execution.
func (s *ConfirmationStore) SetExecutionCall(ctx context.Context, id, callID uuid.UUID) error {
	return s.DB.WithContext(ctx).Model(&models.AIToolConfirmation{}).Where("id = ?", id).Update("execution_call_id", callID).Error
}

// StuckConfirmed lists confirmations left in "confirmed" for longer than minAge that the reconciler may
// still work on (attempts below max and no fresh claim), oldest first.
func (s *ConfirmationStore) StuckConfirmed(ctx context.Context, minAge time.Duration, maxAttempts, limit int) ([]models.AIToolConfirmation, error) {
	cutoff := s.now().Add(-minAge)
	var rows []models.AIToolConfirmation
	err := s.DB.WithContext(ctx).
		Where("status = ? AND confirmed_at < ? AND reconcile_attempts < ? AND (reconcile_claimed_at IS NULL OR reconcile_claimed_at < ?)",
			models.AIConfirmationConfirmed, cutoff, maxAttempts, cutoff).
		Order("confirmed_at ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ExhaustedConfirmed lists confirmations still "confirmed" whose reconciliation attempts ran out.
func (s *ConfirmationStore) ExhaustedConfirmed(ctx context.Context, minAge time.Duration, maxAttempts, limit int) ([]models.AIToolConfirmation, error) {
	cutoff := s.now().Add(-minAge)
	var rows []models.AIToolConfirmation
	err := s.DB.WithContext(ctx).
		Where("status = ? AND reconcile_attempts >= ? AND (reconcile_claimed_at IS NULL OR reconcile_claimed_at < ?)",
			models.AIConfirmationConfirmed, maxAttempts, cutoff).
		Order("confirmed_at ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ClaimReconcile claims one stuck confirmation for one reconciler: a CAS on the claim, so only one
// process works on it. It counts the attempt.
func (s *ConfirmationStore) ClaimReconcile(ctx context.Context, id uuid.UUID, minAge time.Duration) (bool, error) {
	now := s.now()
	cutoff := now.Add(-minAge)
	res := s.DB.WithContext(ctx).Model(&models.AIToolConfirmation{}).
		Where("id = ? AND status = ? AND (reconcile_claimed_at IS NULL OR reconcile_claimed_at < ?)", id, models.AIConfirmationConfirmed, cutoff).
		Updates(map[string]any{"reconcile_claimed_at": now, "reconcile_attempts": gorm.Expr("reconcile_attempts + 1")})
	return res.RowsAffected == 1, res.Error
}

// Get reads one confirmation by id.
func (s *ConfirmationStore) Get(ctx context.Context, id uuid.UUID) (*models.AIToolConfirmation, error) {
	var c models.AIToolConfirmation
	if err := s.DB.WithContext(ctx).First(&c, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}
