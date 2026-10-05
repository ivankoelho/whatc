package aitools

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/models"
)

// What the customer hears when a confirmed request cannot be carried out. Fixed and generic: the
// reason is for the audit, not for the customer.
const (
	DeniedText = "Não foi possível concluir essa solicitação agora."
)

// ConfirmerEnv is the live state the re-authorization reads at the moment of the tap (or of a
// reconciliation). Nothing of the proposal's state is trusted.
type ConfirmerEnv interface {
	GlobalEnabled() bool                                                      // ai_tools.enabled
	WriteEnabled() bool                                                       // ai_tools.write_enabled
	ProviderValidated(ctx context.Context, orgID uuid.UUID) bool              // the organization's CURRENT provider is on ai_tools.providers
	ChatbotEnabled(ctx context.Context, orgID uuid.UUID, account string) bool // the chatbot of that account is on
}

// Notification is a message the reconciler wants sent to a customer after it closed a request that a
// crash had left unfinished.
type Notification struct {
	OrgID, ContactID uuid.UUID
	Account          string
	Text             string
	OutsideHours     bool // send the organization's own out-of-hours message instead of Text
}

// Confirmer turns a customer's tap into the action, once, after re-authorizing it, and closes
// whatever a crash left unfinished. It is the only place a write tool's Execute is called.
type Confirmer struct {
	Store   *ConfirmationStore
	Catalog *Catalog
	Enabled EnabledSource
	Auditor Auditor
	Deps    WriteDeps
	Env     ConfirmerEnv
	// Notify delivers the reconciler's messages (the tap path returns its message to the caller).
	Notify func(ctx context.Context, n Notification)
	// Reconciliation: how old a "confirmed" row must be, and how many attempts it gets.
	ReconcileMinAge      time.Duration
	ReconcileMaxAttempts int
	Log                  Logger
}

// Tap is a customer's tap on a confirmation button.
type Tap struct {
	Token     string
	OrgID     uuid.UUID
	ContactID uuid.UUID // the contact the webhook identified as the sender
	SessionID uuid.UUID // the sender's current chatbot session, from the server, never from the button
	Account   string    // the WhatsApp account that received it
	WAMID     string
	Decline   bool
}

// TapKind says what the caller should do.
type TapKind int

const (
	// TapIgnored: do nothing and say nothing (unknown token, a button that is not pending anymore,
	// a repetition).
	TapIgnored TapKind = iota
	// TapReply: send Text to the customer (or the organization's out-of-hours message if OutsideHours).
	TapReply
)

// TapResult is the outcome of HandleTap.
type TapResult struct {
	Kind         TapKind
	Text         string
	OutsideHours bool
}

func ignored() TapResult          { return TapResult{Kind: TapIgnored} }
func reply(text string) TapResult { return TapResult{Kind: TapReply, Text: text} }
func outsideHours() TapResult     { return TapResult{Kind: TapReply, OutsideHours: true} }

func (c *Confirmer) log(msg string, kv ...any) { logError(c.Log, msg, kv...) }

func (c *Confirmer) humanActor() Actor { return Actor{Kind: ActorHuman, Ref: "customer_confirmation"} }

func (c *Confirmer) writeSpec(name string) (ToolSpec, bool) {
	s, ok := c.Catalog.Get(name)
	return s, ok && s.Risk == RiskWrite && s.WriteFactory != nil && s.Confirm.valid()
}

// reauthorize is the whole security check, done again now. It returns the reason of the first thing
// that no longer holds, or "" when the action may go on. It reads the live state; it consumes nothing.
func (c *Confirmer) reauthorize(ctx context.Context, conf *models.AIToolConfirmation, spec ToolSpec, account string) string {
	if !c.Env.GlobalEnabled() {
		return DenyGlobalOff
	}
	enabled, err := c.Enabled.EnabledTools(ctx, conf.OrganizationID)
	if err != nil { // an unreadable opt-in is no opt-in
		c.log("ai tools: could not read the enabled tools while confirming; denied",
			"organization_id", conf.OrganizationID.String(), "error", err.Error())
		return DenyNotEnabled
	}
	v := Authorize(PolicyInput{
		GlobalEnabled: true, Known: true, OrgEnabled: enabled[spec.Name], Risk: spec.Risk,
		FeatureAllowed: spec.featureAllowed(FeatureChatbotReply), WriteEnabled: c.Env.WriteEnabled(), ConfirmationAvailable: true,
	})
	if !v.Allowed {
		return v.Reason
	}
	if !c.Env.ProviderValidated(ctx, conf.OrganizationID) {
		return models.AIToolDenyProviderNotValidated
	}
	if !c.Env.ChatbotEnabled(ctx, conf.OrganizationID, account) {
		return models.AIToolDenyChatbotDisabled
	}
	return ""
}

func (c *Confirmer) sessionAccount(ctx context.Context, sessionID uuid.UUID) string {
	var account string
	var accounts []string
	c.Store.DB.WithContext(ctx).Table("chatbot_sessions").Where("id = ?", sessionID).Limit(1).Pluck("whats_app_account", &accounts)
	if len(accounts) > 0 {
		account = accounts[0]
	}
	return account
}

// recordDenial writes the audit row of a re-authorization that refused a confirmed-to-be request.
func (c *Confirmer) recordDenial(ctx context.Context, conf *models.AIToolConfirmation, account, reason string) {
	at := c.attempt(conf, account)
	if err := c.Auditor.Denied(ctx, at, reason); err != nil {
		c.log("ai tools: could not record a denied confirmation", "organization_id", conf.OrganizationID.String(), "error", err.Error())
	}
}

func (c *Confirmer) attempt(conf *models.AIToolConfirmation, account string) Attempt {
	contact, session := conf.ContactID, conf.SessionID
	return Attempt{
		RunID: conf.RunID, Step: 1, Risk: RiskWrite, Actor: c.humanActor(),
		Call:  ai.ToolCall{ID: "confirmation:" + conf.ID.String(), Name: conf.ToolName, Arguments: []byte(`{}`)},
		Scope: Scope{OrganizationID: conf.OrganizationID, ContactID: &contact, SessionID: &session, WhatsAppAccount: account},
	}
}

// HandleTap processes a tap on a confirmation button. Everything that does not consume the customer's
// authorization (the lookup, the re-authorization, the state of the transfer) comes BEFORE the
// compare-and-set; the CAS pending -> confirmed is won by exactly one caller; only then is the action
// carried out. A tap on anything that is not pending is inert.
func (c *Confirmer) HandleTap(ctx context.Context, tap Tap) TapResult {
	conf, res := c.Store.Lookup(ctx, tap.Token, tap.OrgID, tap.ContactID, tap.SessionID)
	switch res {
	case LookupNotFound, LookupNotPending:
		return ignored() // never executes, never reconciles, never answers again
	}
	spec, ok := c.writeSpec(conf.ToolName)
	if !ok {
		if won, _ := c.Store.Deny(ctx, conf.ID, DenyUnknownTool); won {
			return reply(DeniedText)
		}
		return ignored()
	}
	account := tap.Account
	if account == "" {
		account = c.sessionAccount(ctx, conf.SessionID)
	}

	if res == LookupExpired {
		if won, _ := c.Store.Expire(ctx, conf.ID); won {
			return reply(spec.Confirm.ExpiredText)
		}
		return ignored()
	}
	if res == LookupTampered {
		if won, _ := c.Store.Deny(ctx, conf.ID, models.AIToolDenyTampered); won {
			c.recordDenial(ctx, conf, account, models.AIToolDenyTampered)
			return reply(DeniedText)
		}
		return ignored()
	}

	if tap.Decline { // a refusal needs no re-authorization
		if won, _ := c.Store.Decline(ctx, conf.ID); won {
			return reply(spec.Confirm.DeclinedText)
		}
		return ignored()
	}

	// Before the CAS: nothing here consumes the authorization.
	if reason := c.reauthorize(ctx, conf, spec, account); reason != "" {
		if won, _ := c.Store.Deny(ctx, conf.ID, reason); won {
			c.recordDenial(ctx, conf, account, reason)
			return reply(DeniedText)
		}
		return ignored()
	}
	if c.Deps.Transfers != nil {
		if c.Deps.Transfers.HasActive(ctx, conf.OrganizationID, conf.ContactID) {
			if won, _ := c.Store.CloseNotExecuted(ctx, conf.ID, models.AIConfirmationOutcomeAlreadyActive); won {
				return reply(spec.Confirm.OutcomeText(models.AIConfirmationOutcomeAlreadyActive))
			}
			return ignored()
		}
		if !c.Deps.Transfers.WithinBusinessHours(ctx, conf.OrganizationID, account) {
			if won, _ := c.Store.CloseNotExecuted(ctx, conf.ID, models.AIConfirmationOutcomeOutsideHours); won {
				return outsideHours()
			}
			return ignored()
		}
	}

	// The CAS. Whoever loses it does nothing.
	won, err := c.Store.Confirm(ctx, conf.ID, tap.ContactID, tap.WAMID)
	if err != nil {
		c.log("ai tools: could not confirm", "organization_id", conf.OrganizationID.String(), "error", err.Error())
		return ignored()
	}
	if !won {
		return ignored()
	}
	confirmed, err := c.Store.Get(ctx, conf.ID)
	if err != nil {
		return ignored() // stays "confirmed"; the reconciler closes it
	}
	return c.executeConfirmed(ctx, confirmed, spec, account)
}

// executeConfirmed carries out an action that is already "confirmed": the single place a write tool's
// Execute runs. It is idempotent by the tool's own contract, so the reconciler may call it again. If
// anything about recording fails the row stays "confirmed" for the reconciler and nothing is repeated.
func (c *Confirmer) executeConfirmed(ctx context.Context, conf *models.AIToolConfirmation, spec ToolSpec, account string) TapResult {
	at := c.attempt(conf, account)
	callID, err := c.Auditor.Requested(ctx, at) // fail-closed, as for every call
	if err != nil {
		c.log("ai tools: could not record the confirmed execution; it was not run (the reconciler will retry)",
			"organization_id", conf.OrganizationID.String(), "confirmation", conf.ID.String(), "error", err.Error())
		return ignored()
	}

	started := time.Now()
	exec, execErr := c.runWrite(ctx, conf, spec, account)
	took := time.Since(started)

	fin := FinishInput{ExecutionCallID: &callID}
	out := Outcome{Status: models.AIToolCallExecuted, Duration: took}
	text := TapResult{}
	switch {
	case execErr != nil:
		fin.Status, fin.ErrorKind = models.AIConfirmationFailed, models.AIConfirmationErrExecution
		out.Status, out.ErrorKind = models.AIToolCallFailed, "tool_error"
		if errors.Is(execErr, context.DeadlineExceeded) {
			out.ErrorKind = "timeout"
		}
		text = reply(DeniedText)
	case exec.Outcome == models.AIConfirmationOutcomeCreated:
		fin.Status, fin.Outcome, fin.TransferID = models.AIConfirmationExecuted, exec.Outcome, exec.TransferID
		text = reply(spec.Confirm.OutcomeText(exec.Outcome))
	case exec.Outcome == models.AIConfirmationOutcomeOutsideHours:
		fin.Status, fin.Outcome = models.AIConfirmationNotExecuted, exec.Outcome
		text = outsideHours()
	default: // already_active
		fin.Status, fin.Outcome = models.AIConfirmationNotExecuted, exec.Outcome
		text = reply(spec.Confirm.OutcomeText(exec.Outcome))
	}

	// The action has happened (or has not): record it. If this fails, it is NOT undone and NOT repeated;
	// the row stays "confirmed" and the reconciler closes it by looking at what exists.
	if _, ferr := c.Store.Finish(ctx, conf.ID, fin); ferr != nil {
		c.log("ai tools: the confirmed action ran but its outcome could not be recorded; the reconciler will close it",
			"organization_id", conf.OrganizationID.String(), "confirmation", conf.ID.String(), "error", ferr.Error())
		return ignored()
	}
	if aerr := c.Auditor.Finish(ctx, callID, out); aerr != nil {
		c.log("ai tools: could not close the audit row of a confirmed execution",
			"organization_id", conf.OrganizationID.String(), "confirmation", conf.ID.String(), "error", aerr.Error())
	}
	return text
}

// runWrite builds the write tool (only now) and runs it, turning a panic into an error.
func (c *Confirmer) runWrite(ctx context.Context, conf *models.AIToolConfirmation, spec ToolSpec, account string) (res ExecResult, err error) {
	defer func() {
		if recover() != nil {
			res, err = ExecResult{}, errors.New("aitools: the write tool panicked")
		}
	}()
	contact, session := conf.ContactID, conf.SessionID
	tool := spec.WriteFactory(Scope{OrganizationID: conf.OrganizationID, ContactID: &contact, SessionID: &session, WhatsAppAccount: account}, c.Deps)
	if tool == nil {
		return ExecResult{}, errors.New("aitools: no write tool")
	}
	confirmedAt := time.Now()
	if conf.ConfirmedAt != nil {
		confirmedAt = *conf.ConfirmedAt
	}
	return tool.Execute(ctx, conf.Args, confirmedAt)
}

// Reconcile is one sweep of the reconciler: it closes "confirmed" requests that a crash left
// unfinished. It is recovery infrastructure, not a second path of authorization: a request is
// reconciled only if its authorization is not older than the confirmation validity and only if the
// whole re-authorization still passes. It returns how many requests it worked on.
func (c *Confirmer) Reconcile(ctx context.Context) int {
	minAge, attempts := c.ReconcileMinAge, c.ReconcileMaxAttempts

	// those whose attempts ran out
	if rows, err := c.Store.ExhaustedConfirmed(ctx, minAge, attempts, 50); err == nil {
		for i := range rows {
			if won, _ := c.Store.Finish(ctx, rows[i].ID, FinishInput{Status: models.AIConfirmationFailed, ErrorKind: models.AIConfirmationErrReconcileExhausted}); won {
				c.log("ai tools: a confirmed request could not be reconciled and was failed",
					"organization_id", rows[i].OrganizationID.String(), "confirmation", rows[i].ID.String())
			}
		}
	}

	rows, err := c.Store.StuckConfirmed(ctx, minAge, attempts, 50)
	if err != nil {
		c.log("ai tools: the reconciler could not list stuck confirmations", "error", err.Error())
		return 0
	}
	worked := 0
	for i := range rows {
		won, err := c.Store.ClaimReconcile(ctx, rows[i].ID, minAge)
		if err != nil || !won {
			continue // another reconciler has it
		}
		worked++
		c.reconcileOne(ctx, rows[i].ID)
	}
	return worked
}

func (c *Confirmer) reconcileOne(ctx context.Context, id uuid.UUID) {
	conf, err := c.Store.Get(ctx, id)
	if err != nil || conf.Status != models.AIConfirmationConfirmed || conf.ConfirmedAt == nil {
		return
	}
	account := c.sessionAccount(ctx, conf.SessionID)
	spec, ok := c.writeSpec(conf.ToolName)
	notify := func(text string, outside bool) {
		if c.Notify != nil && (text != "" || outside) {
			c.Notify(ctx, Notification{OrgID: conf.OrganizationID, ContactID: conf.ContactID, Account: account, Text: text, OutsideHours: outside})
		}
	}
	if !ok {
		_, _ = c.Store.Finish(ctx, conf.ID, FinishInput{Status: models.AIConfirmationDenied, DenialReason: DenyUnknownTool})
		return
	}

	// 1. the authorization must not be older than the validity of a confirmation
	if c.Store.now().Sub(*conf.ConfirmedAt) > c.Store.Params.TTL {
		_, _ = c.Store.Finish(ctx, conf.ID, FinishInput{Status: models.AIConfirmationFailed, ErrorKind: models.AIConfirmationErrReconcileStale})
		return
	}
	// 2. the whole re-authorization, again: reconciling never widens the authority of the tap
	if reason := c.reauthorize(ctx, conf, spec, account); reason != "" {
		if won, _ := c.Store.Finish(ctx, conf.ID, FinishInput{Status: models.AIConfirmationDenied, DenialReason: reason}); won {
			c.recordDenial(ctx, conf, account, reason)
			notify(DeniedText, false)
		}
		return
	}
	// 3. what already exists: the execution may have created the transfer before the crash
	if c.Deps.Transfers != nil {
		if tid, found := c.Deps.Transfers.FindAITransfer(ctx, conf.OrganizationID, conf.ContactID, *conf.ConfirmedAt); found {
			if won, _ := c.Store.Finish(ctx, conf.ID, FinishInput{Status: models.AIConfirmationExecuted, Outcome: models.AIConfirmationOutcomeCreated, TransferID: &tid}); won {
				notify(spec.Confirm.OutcomeText(models.AIConfirmationOutcomeCreated), false)
			}
			return
		}
	}
	// 4. otherwise carry it out, idempotently
	r := c.executeConfirmed(ctx, conf, spec, account)
	if r.Kind == TapReply {
		notify(r.Text, r.OutsideHours)
	}
}
