package aitools_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockedTransfers is a TransferService double that is safe under concurrent use.
type lockedTransfers struct {
	mu          sync.Mutex
	active      bool
	inHours     bool
	result      aitools.ExecResult
	err         error
	panics      bool
	existing    *uuid.UUID // an AI transfer that "already exists" for FindAITransfer
	transferred int
	notes       []string
}

func (f *lockedTransfers) HasActive(context.Context, uuid.UUID, uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}
func (f *lockedTransfers) WithinBusinessHours(context.Context, uuid.UUID, string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inHours
}
func (f *lockedTransfers) FindAITransfer(context.Context, uuid.UUID, uuid.UUID, time.Time) (uuid.UUID, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.existing != nil {
		return *f.existing, true
	}
	return uuid.Nil, false
}
func (f *lockedTransfers) TransferToQueue(_ context.Context, _ aitools.Scope, notes string) (aitools.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panics {
		panic("boom")
	}
	f.transferred++
	f.notes = append(f.notes, notes)
	return f.result, f.err
}
func (f *lockedTransfers) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transferred
}

type fakeEnv struct {
	global, write, provider, chatbot atomic.Bool
}

func newFakeEnv() *fakeEnv {
	e := &fakeEnv{}
	e.global.Store(true)
	e.write.Store(true)
	e.provider.Store(true)
	e.chatbot.Store(true)
	return e
}
func (e *fakeEnv) GlobalEnabled() bool                                    { return e.global.Load() }
func (e *fakeEnv) WriteEnabled() bool                                     { return e.write.Load() }
func (e *fakeEnv) ProviderValidated(context.Context, uuid.UUID) bool      { return e.provider.Load() }
func (e *fakeEnv) ChatbotEnabled(context.Context, uuid.UUID, string) bool { return e.chatbot.Load() }

// downAuditor wraps an Auditor and can be switched to fail.
type downAuditor struct {
	aitools.Auditor
	down atomic.Bool
}

func (d *downAuditor) Requested(ctx context.Context, a aitools.Attempt) (uuid.UUID, error) {
	if d.down.Load() {
		return uuid.Nil, errors.New("audit down")
	}
	return d.Auditor.Requested(ctx, a)
}

type notice struct {
	text    string
	outside bool
}

type writeEnv struct {
	*confEnv
	transfers *lockedTransfers
	env       *fakeEnv
	auditor   *downAuditor
	catalog   *aitools.Catalog
	confirmer *aitools.Confirmer
	notices   []notice
	nmu       sync.Mutex
	tid       uuid.UUID
}

func newWriteEnv(t *testing.T) *writeEnv {
	t.Helper()
	ce := newConfEnv(t, defaultParams())
	// The reconciler is global by design: start from (and leave behind) a clean table so a request
	// stuck in another test can never be picked up by this one.
	require.NoError(t, ce.db.Exec("DELETE FROM ai_tool_confirmations").Error)
	t.Cleanup(func() { ce.db.Exec("DELETE FROM ai_tool_confirmations") })
	w := &writeEnv{confEnv: ce, env: newFakeEnv(), tid: uuid.New()}
	w.transfers = &lockedTransfers{inHours: true}
	w.transfers.result = aitools.ExecResult{Outcome: models.AIConfirmationOutcomeCreated, TransferID: &w.tid}
	w.auditor = &downAuditor{Auditor: aitools.DBAuditor{DB: ce.db, Secret: "server-secret"}}
	cat, err := aitools.NewCatalog(aitools.NewRequestAgentTransferSpec())
	require.NoError(t, err)
	w.catalog = cat
	require.NoError(t, ce.db.Create(&models.AIToolSetting{OrganizationID: ce.org.ID, ToolName: "request_agent_transfer", Enabled: true}).Error)

	w.confirmer = &aitools.Confirmer{
		Store: ce.store, Catalog: cat, Enabled: aitools.SettingsStore{DB: ce.db}, Auditor: w.auditor,
		Deps: aitools.WriteDeps{Transfers: w.transfers}, Env: w.env,
		ReconcileMinAge: 60 * time.Second, ReconcileMaxAttempts: 3,
		Notify: func(_ context.Context, n aitools.Notification) {
			w.nmu.Lock()
			defer w.nmu.Unlock()
			w.notices = append(w.notices, notice{n.Text, n.OutsideHours})
		},
	}
	return w
}

func (w *writeEnv) resolver(feature string) *aitools.Resolver {
	return aitools.NewResolver(context.Background(), aitools.ResolverConfig{
		Catalog: w.catalog, GlobalEnabled: true, Enabled: map[string]bool{"request_agent_transfer": true}, Auditor: w.auditor,
		Actor:        aitools.AIActor(feature),
		Scope:        aitools.Scope{OrganizationID: w.org.ID, ContactID: &w.contact.ID, SessionID: &w.session.ID, WhatsAppAccount: "acc"},
		WriteEnabled: true, ConfirmationAvailable: true, Confirmations: w.store, WriteDeps: aitools.WriteDeps{Transfers: w.transfers},
	}, nil)
}

// proposeViaTheAI runs the model's call through the governed resolver.
func (w *writeEnv) proposeViaTheAI(t *testing.T, args string) (ai.ToolResult, *aitools.PendingConfirmation) {
	t.Helper()
	r := w.resolver("chatbot_reply")
	tool, _ := r.Resolve("request_agent_transfer")
	res, err := tool.Execute(context.Background(), ai.ToolCall{ID: "call_1", Name: "request_agent_transfer", Arguments: json.RawMessage(args)})
	require.NoError(t, err)
	return res, r.Pending()
}

func (w *writeEnv) tap(token string, decline bool) aitools.TapResult {
	return w.confirmer.HandleTap(context.Background(), aitools.Tap{
		Token: token, OrgID: w.org.ID, ContactID: w.contact.ID, SessionID: w.session.ID, Account: "acc", WAMID: "wamid.tap", Decline: decline,
	})
}

func (w *writeEnv) conf(t *testing.T, id uuid.UUID) *models.AIToolConfirmation {
	t.Helper()
	c, err := w.store.Get(context.Background(), id)
	require.NoError(t, err)
	return c
}

func (w *writeEnv) callRows(t *testing.T) []models.AIToolCall {
	t.Helper()
	var rows []models.AIToolCall
	require.NoError(t, w.db.Where("organization_id = ?", w.org.ID).Order("requested_at ASC").Find(&rows).Error)
	return rows
}

// --- the model's call only proposes ---

func TestWritePath_TheModelsCallProposesAndNeverTransfers(t *testing.T) {
	w := newWriteEnv(t)
	res, pending := w.proposeViaTheAI(t, `{"reason":"pedido atrasado"}`)

	assert.False(t, res.IsError)
	assert.JSONEq(t, `{"status":"awaiting_customer_confirmation","transfer_performed":false}`, res.Content)
	require.NotNil(t, pending)
	assert.Len(t, pending.Token, 22)
	assert.Equal(t, models.AIConfirmationPending, pending.Confirmation.Status)
	assert.NotNil(t, pending.Spec)
	assert.Equal(t, 10*time.Minute, pending.TTL)
	assert.Zero(t, w.transfers.count(), "the proposal transfers nothing")

	c := w.conf(t, pending.Confirmation.ID)
	assert.Equal(t, models.JSONB{"reason": "pedido atrasado"}, c.Args)
	assert.Equal(t, w.contact.ID, c.ContactID)
	assert.Equal(t, w.session.ID, c.SessionID)

	// audit: one row, the AI proposed (executed here only means the proposal was created)
	rows := w.callRows(t)
	require.Len(t, rows, 1)
	assert.Equal(t, "ai", rows[0].ActorKind)
	assert.Equal(t, "chatbot_reply", rows[0].ActorRef)
	assert.Equal(t, "write", rows[0].Risk)
	assert.Equal(t, models.AIToolCallExecuted, rows[0].Status)
	require.NotNil(t, c.ProposalCallID)
	assert.Equal(t, rows[0].ID, *c.ProposalCallID, "the confirmation points at the proposing call")
}

func TestWritePath_ProposalRefusalsAreAnswersAndCreateNoConfirmation(t *testing.T) {
	w := newWriteEnv(t)

	res, pending := w.proposeViaTheAI(t, `{"team_id":"x"}`)
	assert.True(t, res.IsError)
	assert.Nil(t, pending)

	w.transfers.active = true
	res, pending = w.proposeViaTheAI(t, `{}`)
	assert.Contains(t, res.Content, "already_with_agent")
	assert.Nil(t, pending)
	w.transfers.active = false

	w.transfers.inHours = false
	res, pending = w.proposeViaTheAI(t, `{}`)
	assert.Contains(t, res.Content, "outside_business_hours")
	assert.Nil(t, pending)
	w.transfers.inHours = true

	var n int64
	require.NoError(t, w.db.Model(&models.AIToolConfirmation{}).Where("organization_id = ?", w.org.ID).Count(&n).Error)
	assert.Zero(t, n)
	assert.Zero(t, w.transfers.count())
}

func TestWritePath_LimitAndCooldownAreAnswersToTheModel(t *testing.T) {
	w := newWriteEnv(t)
	for i := 0; i < 3; i++ {
		_, p := w.proposeViaTheAI(t, `{}`)
		require.NotNil(t, p, "proposal %d", i+1)
	}
	res, p := w.proposeViaTheAI(t, `{}`)
	assert.Nil(t, p)
	assert.Contains(t, res.Content, "too_many_requests")
	assert.Contains(t, res.Content, `"transfer_performed":false`)

	w2 := newWriteEnv(t)
	_, p = w2.proposeViaTheAI(t, `{}`)
	require.NotNil(t, p)
	require.Equal(t, aitools.TapReply, w2.tap(p.Token, true).Kind)
	res, p = w2.proposeViaTheAI(t, `{}`)
	assert.Nil(t, p)
	assert.Contains(t, res.Content, "recently_declined")
}

func TestWritePath_WithoutAStoreTheWriteToolIsRefusedAndNothingIsBuilt(t *testing.T) {
	w := newWriteEnv(t)
	built := 0
	spec := aitools.NewRequestAgentTransferSpec()
	orig := spec.WriteFactory
	spec.WriteFactory = func(s aitools.Scope, d aitools.WriteDeps) aitools.WriteTool { built++; return orig(s, d) }
	cat, err := aitools.NewCatalog(spec)
	require.NoError(t, err)
	r := aitools.NewResolver(context.Background(), aitools.ResolverConfig{
		Catalog: cat, GlobalEnabled: true, Enabled: map[string]bool{"request_agent_transfer": true}, Auditor: w.auditor,
		Actor: aitools.AIActor("chatbot_reply"), Scope: aitools.Scope{OrganizationID: w.org.ID, ContactID: &w.contact.ID, SessionID: &w.session.ID},
		WriteEnabled: true, ConfirmationAvailable: true, // no store
	}, nil)
	tool, _ := r.Resolve("request_agent_transfer")
	res, err := tool.Execute(context.Background(), ai.ToolCall{ID: "c", Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Zero(t, built)
}

func TestWritePath_AuditDownMeansNothingIsProposed(t *testing.T) {
	w := newWriteEnv(t)
	w.auditor.down.Store(true)
	res, p := w.proposeViaTheAI(t, `{}`)
	assert.True(t, res.IsError)
	assert.Nil(t, p)
	var n int64
	require.NoError(t, w.db.Model(&models.AIToolConfirmation{}).Where("organization_id = ?", w.org.ID).Count(&n).Error)
	assert.Zero(t, n, "fail-closed, as for every call")
}

// --- the customer's tap ---

func TestTap_HappyPathCarriesOutOnceAndRecordsEveryone(t *testing.T) {
	w := newWriteEnv(t)
	_, p := w.proposeViaTheAI(t, `{"reason":"preciso de ajuda"}`)
	require.NotNil(t, p)

	got := w.tap(p.Token, false)
	assert.Equal(t, aitools.TapReply, got.Kind)
	assert.Contains(t, got.Text, "fila de atendimento")
	assert.Equal(t, 1, w.transfers.count())
	require.Len(t, w.transfers.notes, 1)
	assert.Contains(t, w.transfers.notes[0], "confirmado pelo cliente")
	assert.Contains(t, w.transfers.notes[0], "Motivo informado pela IA (não verificado): preciso de ajuda")

	c := w.conf(t, p.Confirmation.ID)
	assert.Equal(t, models.AIConfirmationExecuted, c.Status)
	assert.Equal(t, models.AIConfirmationOutcomeCreated, c.Outcome)
	require.NotNil(t, c.TransferID)
	assert.Equal(t, w.tid, *c.TransferID)
	require.NotNil(t, c.ConfirmedByContactID)
	assert.Equal(t, w.contact.ID, *c.ConfirmedByContactID, "the customer is who confirmed")
	assert.Equal(t, "wamid.tap", c.ConfirmWAMID)
	assert.NotNil(t, c.ConfirmedAt)
	assert.NotNil(t, c.FinishedAt)

	// two audit rows: the AI proposed, the customer authorized the execution
	rows := w.callRows(t)
	require.Len(t, rows, 2)
	assert.Equal(t, "ai", rows[0].ActorKind)
	assert.Equal(t, "human", rows[1].ActorKind)
	assert.Equal(t, "customer_confirmation", rows[1].ActorRef)
	assert.Equal(t, models.AIToolCallExecuted, rows[1].Status)
	assert.Equal(t, w.contact.ID, *rows[1].SubjectContactID)
	require.NotNil(t, c.ExecutionCallID)
	assert.Equal(t, rows[1].ID, *c.ExecutionCallID)
	assert.NotEqual(t, rows[0].ID, rows[1].ID)
}

func TestTap_ARepeatedOrConcurrentTapDoesNotTransferTwice(t *testing.T) {
	w := newWriteEnv(t)
	_, p := w.proposeViaTheAI(t, `{}`)
	require.NotNil(t, p)

	var replies int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w.tap(p.Token, false).Kind == aitools.TapReply {
				atomic.AddInt32(&replies, 1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, w.transfers.count(), "exactly one transfer however many taps")
	assert.EqualValues(t, 1, replies, "and at most one message")
	assert.Equal(t, aitools.TapIgnored, w.tap(p.Token, false).Kind, "a later tap says nothing")
	assert.Equal(t, 1, w.transfers.count())
}

func TestTap_AButtonOnAConfirmedRequestIsInertNeverARecovery(t *testing.T) {
	w := newWriteEnv(t)
	_, p := w.proposeViaTheAI(t, `{}`)
	require.NotNil(t, p)
	// the authorization was consumed and the process "crashed" before executing
	ok, err := w.store.Confirm(context.Background(), p.Confirmation.ID, w.contact.ID, "wamid.first")
	require.NoError(t, err)
	require.True(t, ok)

	got := w.tap(p.Token, false)
	assert.Equal(t, aitools.TapIgnored, got.Kind, "the token authorizes nothing after the CAS")
	assert.Zero(t, w.transfers.count(), "no execution, no reconciliation from the button")
	assert.Equal(t, models.AIConfirmationConfirmed, w.conf(t, p.Confirmation.ID).Status)
	assert.Equal(t, aitools.TapIgnored, w.tap(p.Token, true).Kind, "nor does the other button")
}

func TestTap_DeclineExpiredSupersededAndForeignTaps(t *testing.T) {
	w := newWriteEnv(t)

	_, p := w.proposeViaTheAI(t, `{}`)
	require.NotNil(t, p)
	got := w.tap(p.Token, true)
	assert.Equal(t, aitools.TapReply, got.Kind)
	assert.Equal(t, "Tudo bem, seguimos por aqui.", got.Text)
	assert.Equal(t, models.AIConfirmationDeclined, w.conf(t, p.Confirmation.ID).Status)
	assert.Equal(t, aitools.TapIgnored, w.tap(p.Token, false).Kind, "a declined request cannot be confirmed afterwards")
	assert.Zero(t, w.transfers.count())

	// expired: answered once, then inert
	w2 := newWriteEnv(t)
	_, p2 := w2.proposeViaTheAI(t, `{}`)
	require.NotNil(t, p2)
	w2.advance(11 * time.Minute)
	got = w2.tap(p2.Token, false)
	assert.Equal(t, aitools.TapReply, got.Kind)
	assert.Contains(t, got.Text, "expirou")
	assert.Equal(t, models.AIConfirmationExpired, w2.conf(t, p2.Confirmation.ID).Status)
	assert.Equal(t, aitools.TapIgnored, w2.tap(p2.Token, false).Kind)
	assert.Zero(t, w2.transfers.count())

	// superseded
	w3 := newWriteEnv(t)
	_, first := w3.proposeViaTheAI(t, `{}`)
	_, second := w3.proposeViaTheAI(t, `{}`)
	require.NotNil(t, first)
	require.NotNil(t, second)
	assert.Equal(t, aitools.TapIgnored, w3.tap(first.Token, false).Kind)
	assert.Zero(t, w3.transfers.count())
	assert.Equal(t, aitools.TapReply, w3.tap(second.Token, false).Kind)
	assert.Equal(t, 1, w3.transfers.count())

	// someone else's tap
	w4 := newWriteEnv(t)
	_, p4 := w4.proposeViaTheAI(t, `{}`)
	other, _ := w4.newContact(t, w4.org)
	res := w4.confirmer.HandleTap(context.Background(), aitools.Tap{Token: p4.Token, OrgID: w4.org.ID, ContactID: other.ID, SessionID: w4.session.ID, Account: "acc"})
	assert.Equal(t, aitools.TapIgnored, res.Kind)
	assert.Zero(t, w4.transfers.count())
	assert.Equal(t, models.AIConfirmationPending, w4.conf(t, p4.Confirmation.ID).Status)

	// the same contact, but another session: indistinguishable from an unknown token, nothing consumed
	res = w4.confirmer.HandleTap(context.Background(), aitools.Tap{Token: p4.Token, OrgID: w4.org.ID, ContactID: w4.contact.ID, SessionID: uuid.New(), Account: "acc"})
	assert.Equal(t, aitools.TapIgnored, res.Kind)
	assert.Zero(t, w4.transfers.count())
	assert.Equal(t, models.AIConfirmationPending, w4.conf(t, p4.Confirmation.ID).Status)
}

func TestTap_TamperedActionIsDeniedAndNothingRuns(t *testing.T) {
	w := newWriteEnv(t)
	_, p := w.proposeViaTheAI(t, `{"reason":"original"}`)
	require.NoError(t, w.db.Model(&models.AIToolConfirmation{}).Where("id = ?", p.Confirmation.ID).Update("args", models.JSONB{"reason": "altered"}).Error)

	got := w.tap(p.Token, false)
	assert.Equal(t, aitools.TapReply, got.Kind)
	assert.Equal(t, aitools.DeniedText, got.Text)
	c := w.conf(t, p.Confirmation.ID)
	assert.Equal(t, models.AIConfirmationDenied, c.Status)
	assert.Equal(t, "tampered", c.DenialReason)
	assert.Nil(t, c.ConfirmedAt)
	assert.Zero(t, w.transfers.count())
}

// --- re-authorization: before the CAS, consuming nothing ---

func TestTap_ReauthorizationBeforeTheCASClosesTheProposalWithoutConfirmingIt(t *testing.T) {
	cases := map[string]struct {
		off    func(w *writeEnv)
		reason string
	}{
		"global switch off":      {func(w *writeEnv) { w.env.global.Store(false) }, aitools.DenyGlobalOff},
		"write_enabled off":      {func(w *writeEnv) { w.env.write.Store(false) }, aitools.DenyWriteDisabled},
		"opt-in removed":         {func(w *writeEnv) { w.db.Where("organization_id = ?", w.org.ID).Delete(&models.AIToolSetting{}) }, aitools.DenyNotEnabled},
		"provider not validated": {func(w *writeEnv) { w.env.provider.Store(false) }, models.AIToolDenyProviderNotValidated},
		"chatbot disabled":       {func(w *writeEnv) { w.env.chatbot.Store(false) }, models.AIToolDenyChatbotDisabled},
	}
	for name, c := range cases {
		w := newWriteEnv(t)
		_, p := w.proposeViaTheAI(t, `{}`)
		require.NotNil(t, p, name)
		c.off(w) // something changed between the proposal and the tap

		got := w.tap(p.Token, false)
		assert.Equal(t, aitools.TapReply, got.Kind, name)
		assert.Equal(t, aitools.DeniedText, got.Text, name)
		conf := w.conf(t, p.Confirmation.ID)
		assert.Equal(t, models.AIConfirmationDenied, conf.Status, name)
		assert.Equal(t, c.reason, conf.DenialReason, name)
		assert.Nil(t, conf.ConfirmedAt, name+": it never passed through confirmed")
		assert.Zero(t, w.transfers.count(), name)

		// the denial is on record, attributed to the customer's confirmation attempt
		rows := w.callRows(t)
		require.Len(t, rows, 2, name)
		assert.Equal(t, models.AIToolCallDenied, rows[1].Status, name)
		assert.Equal(t, c.reason, rows[1].DenialReason, name)
		assert.Equal(t, "human", rows[1].ActorKind, name)
	}
}

func TestTap_StateChecksBeforeTheCASCloseNothingConsumed(t *testing.T) {
	w := newWriteEnv(t)
	_, p := w.proposeViaTheAI(t, `{}`)
	w.transfers.active = true // someone else created a transfer meanwhile
	got := w.tap(p.Token, false)
	assert.Equal(t, aitools.TapReply, got.Kind)
	assert.Contains(t, got.Text, "já está sendo atendido")
	c := w.conf(t, p.Confirmation.ID)
	assert.Equal(t, models.AIConfirmationNotExecuted, c.Status)
	assert.Equal(t, models.AIConfirmationOutcomeAlreadyActive, c.Outcome)
	assert.Nil(t, c.ConfirmedAt)
	assert.Zero(t, w.transfers.count())

	w2 := newWriteEnv(t)
	_, p2 := w2.proposeViaTheAI(t, `{}`)
	w2.transfers.inHours = false
	got = w2.tap(p2.Token, false)
	assert.Equal(t, aitools.TapReply, got.Kind)
	assert.True(t, got.OutsideHours, "the organization's own out-of-hours message")
	assert.Empty(t, got.Text)
	c = w2.conf(t, p2.Confirmation.ID)
	assert.Equal(t, models.AIConfirmationNotExecuted, c.Status)
	assert.Equal(t, models.AIConfirmationOutcomeOutsideHours, c.Outcome)
	assert.Zero(t, w2.transfers.count())
}

// --- what the service answers after the CAS ---

func TestTap_OutcomesAfterTheCAS(t *testing.T) {
	// the race the pre-checks cannot close: the service itself says already_active / outside_hours
	for outcome, text := range map[string]string{
		models.AIConfirmationOutcomeAlreadyActive: "já está sendo atendido",
		models.AIConfirmationOutcomeOutsideHours:  "",
	} {
		w := newWriteEnv(t)
		w.transfers.result = aitools.ExecResult{Outcome: outcome}
		_, p := w.proposeViaTheAI(t, `{}`)
		got := w.tap(p.Token, false)
		assert.Equal(t, aitools.TapReply, got.Kind, outcome)
		if text != "" {
			assert.Contains(t, got.Text, text)
		} else {
			assert.True(t, got.OutsideHours)
		}
		c := w.conf(t, p.Confirmation.ID)
		assert.Equal(t, models.AIConfirmationNotExecuted, c.Status, outcome)
		assert.Equal(t, outcome, c.Outcome)
		assert.NotNil(t, c.ConfirmedAt, "it was confirmed, and nothing was created")
		assert.Nil(t, c.TransferID)
		assert.Equal(t, 1, w.transfers.count())
	}

	// a failure and a panic are "failed", generic to the customer
	for name, mutate := range map[string]func(w *writeEnv){
		"error": func(w *writeEnv) { w.transfers.err = errors.New("db down") },
		"panic": func(w *writeEnv) { w.transfers.panics = true },
	} {
		w := newWriteEnv(t)
		mutate(w)
		_, p := w.proposeViaTheAI(t, `{}`)
		got := w.tap(p.Token, false)
		assert.Equal(t, aitools.DeniedText, got.Text, name)
		c := w.conf(t, p.Confirmation.ID)
		assert.Equal(t, models.AIConfirmationFailed, c.Status, name)
		assert.Equal(t, models.AIConfirmationErrExecution, c.ErrorKind, name)
		rows := w.callRows(t)
		require.Len(t, rows, 2, name)
		assert.Equal(t, models.AIToolCallFailed, rows[1].Status, name)
	}
}

func TestTap_AuditDownAfterTheCASLeavesItConfirmedAndExecutesNothing(t *testing.T) {
	w := newWriteEnv(t)
	_, p := w.proposeViaTheAI(t, `{}`)
	w.auditor.down.Store(true)

	got := w.tap(p.Token, false)
	assert.Equal(t, aitools.TapIgnored, got.Kind)
	assert.Zero(t, w.transfers.count(), "fail-closed: no audit, no execution")
	assert.Equal(t, models.AIConfirmationConfirmed, w.conf(t, p.Confirmation.ID).Status, "left for the reconciler")

	// the audit comes back: the reconciler finishes it, once
	w.auditor.down.Store(false)
	w.advance(61 * time.Second)
	assert.Equal(t, 1, w.confirmer.Reconcile(context.Background()))
	assert.Equal(t, 1, w.transfers.count())
	assert.Equal(t, models.AIConfirmationExecuted, w.conf(t, p.Confirmation.ID).Status)
}

// --- the reconciler: recovery, never a second authorization ---

func (w *writeEnv) stuck(t *testing.T) *aitools.PendingConfirmation {
	t.Helper()
	_, p := w.proposeViaTheAI(t, `{"reason":"x"}`)
	require.NotNil(t, p)
	ok, err := w.store.Confirm(context.Background(), p.Confirmation.ID, w.contact.ID, "wamid.first")
	require.NoError(t, err)
	require.True(t, ok)
	return p
}

func TestReconcile_ExecutesAStuckConfirmationOnceAndTellsTheCustomer(t *testing.T) {
	w := newWriteEnv(t)
	p := w.stuck(t)

	assert.Zero(t, w.confirmer.Reconcile(context.Background()), "too recent: the normal path may still be running")
	assert.Zero(t, w.transfers.count())

	w.advance(61 * time.Second)
	assert.Equal(t, 1, w.confirmer.Reconcile(context.Background()))
	assert.Equal(t, 1, w.transfers.count())
	c := w.conf(t, p.Confirmation.ID)
	assert.Equal(t, models.AIConfirmationExecuted, c.Status)
	assert.Equal(t, w.tid, *c.TransferID)
	assert.Equal(t, 1, c.ReconcileAttempts)
	require.Len(t, w.notices, 1)
	assert.Contains(t, w.notices[0].text, "fila de atendimento")

	assert.Zero(t, w.confirmer.Reconcile(context.Background()), "nothing left to do")
	assert.Equal(t, 1, w.transfers.count())
}

func TestReconcile_AnExistingTransferIsRecordedNotCreatedAgain(t *testing.T) {
	w := newWriteEnv(t)
	existing := uuid.New()
	w.transfers.existing = &existing
	p := w.stuck(t)
	w.advance(61 * time.Second)

	assert.Equal(t, 1, w.confirmer.Reconcile(context.Background()))
	assert.Zero(t, w.transfers.count(), "the transfer the crashed run had created is found, not duplicated")
	c := w.conf(t, p.Confirmation.ID)
	assert.Equal(t, models.AIConfirmationExecuted, c.Status)
	assert.Equal(t, existing, *c.TransferID)
}

func TestReconcile_NeverWidensTheAuthority(t *testing.T) {
	// too old: the customer's authorization is stale
	w := newWriteEnv(t)
	p := w.stuck(t)
	w.advance(11 * time.Minute)
	w.confirmer.Reconcile(context.Background())
	c := w.conf(t, p.Confirmation.ID)
	assert.Equal(t, models.AIConfirmationFailed, c.Status)
	assert.Equal(t, models.AIConfirmationErrReconcileStale, c.ErrorKind)
	assert.Zero(t, w.transfers.count())

	// re-authorization refuses: the write switch was turned off after the tap
	w2 := newWriteEnv(t)
	p2 := w2.stuck(t)
	w2.env.write.Store(false)
	w2.advance(61 * time.Second)
	w2.confirmer.Reconcile(context.Background())
	c2 := w2.conf(t, p2.Confirmation.ID)
	assert.Equal(t, models.AIConfirmationDenied, c2.Status)
	assert.Equal(t, aitools.DenyWriteDisabled, c2.DenialReason)
	assert.Zero(t, w2.transfers.count())
	require.Len(t, w2.notices, 1)
	assert.Equal(t, aitools.DeniedText, w2.notices[0].text)
}

func TestReconcile_AttemptsRunOutAndConcurrentReconcilersWorkOnce(t *testing.T) {
	w := newWriteEnv(t)
	w.transfers.err = errors.New("db down") // the one attempt that gets claimed fails and is closed as failed
	p := w.stuck(t)
	w.advance(61 * time.Second)

	// two reconcilers at once: one claims, the other does not
	var wg sync.WaitGroup
	var worked int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); atomic.AddInt32(&worked, int32(w.confirmer.Reconcile(context.Background()))) }()
	}
	wg.Wait()
	assert.EqualValues(t, 1, worked, "one reconciler per request")
	assert.Equal(t, 1, w.transfers.count(), "a single execution attempt")
	assert.Equal(t, models.AIConfirmationFailed, w.conf(t, p.Confirmation.ID).Status)

	// exhaustion: a request that keeps being claimed without finishing
	w2 := newWriteEnv(t)
	p2 := w2.stuck(t)
	for i := 0; i < 3; i++ {
		w2.advance(61 * time.Second)
		won, err := w2.store.ClaimReconcile(context.Background(), p2.Confirmation.ID, 60*time.Second)
		require.NoError(t, err)
		require.True(t, won)
	}
	w2.advance(61 * time.Second)
	w2.confirmer.Reconcile(context.Background())
	c := w2.conf(t, p2.Confirmation.ID)
	assert.Equal(t, models.AIConfirmationFailed, c.Status)
	assert.Equal(t, models.AIConfirmationErrReconcileExhausted, c.ErrorKind)
	assert.Zero(t, w2.transfers.count())
}
