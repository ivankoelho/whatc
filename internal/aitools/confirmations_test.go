package aitools_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type confEnv struct {
	db      *gorm.DB
	org     *models.Organization
	contact *models.Contact
	session *models.ChatbotSession
	clock   *time.Time
	store   *aitools.ConfirmationStore
}

func newConfEnv(t *testing.T, params aitools.ConfirmationParams) *confEnv {
	t.Helper()
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	e := &confEnv{db: db, org: org}
	e.contact, e.session = e.newContact(t, org)
	now := time.Now()
	e.clock = &now
	e.store = &aitools.ConfirmationStore{DB: db, Secret: "server-secret", Params: params, Now: func() time.Time { return *e.clock }}
	return e
}

func (e *confEnv) newContact(t *testing.T, org *models.Organization) (*models.Contact, *models.ChatbotSession) {
	t.Helper()
	c := testutil.CreateTestContact(t, e.db, org.ID)
	s := &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: c.ID,
		WhatsAppAccount: "acc", PhoneNumber: c.PhoneNumber, Status: models.SessionStatusActive, LastActivityAt: time.Now()}
	require.NoError(t, e.db.Create(s).Error)
	return c, s
}

func (e *confEnv) advance(d time.Duration) { *e.clock = e.clock.Add(d) }

func defaultParams() aitools.ConfirmationParams {
	return aitools.ConfirmationParams{TTL: 10 * time.Minute, Limit: 3, Window: 60 * time.Minute, Cooldown: 30 * time.Minute}
}

func (e *confEnv) propose(t *testing.T, args models.JSONB) (string, models.AIToolConfirmation) {
	t.Helper()
	tok, c, err := e.store.Propose(context.Background(), e.input(args))
	require.NoError(t, err)
	return tok, c
}

func (e *confEnv) input(args models.JSONB) aitools.ProposeInput {
	return aitools.ProposeInput{
		Scope: aitools.Scope{OrganizationID: e.org.ID, ContactID: &e.contact.ID, SessionID: &e.session.ID},
		RunID: uuid.New(), Tool: "request_agent_transfer", Args: args,
	}
}

func (e *confEnv) lookup(token string) (*models.AIToolConfirmation, aitools.LookupResult) {
	return e.store.Lookup(context.Background(), token, e.org.ID, e.contact.ID, e.session.ID)
}

func TestConfirmation_ProposeStoresOnlyTheHashAndBindsTheAction(t *testing.T) {
	e := newConfEnv(t, defaultParams())
	tok, c := e.propose(t, models.JSONB{"reason": "quero uma pessoa"})

	assert.Len(t, tok, 22, "128 bits, base64url")
	assert.Equal(t, aitools.HashToken(tok), c.TokenHash)
	assert.Len(t, c.TokenHash, 64)
	assert.Equal(t, models.AIConfirmationPending, c.Status)
	assert.WithinDuration(t, e.clock.Add(10*time.Minute), c.ExpiresAt, time.Second)
	assert.NotEmpty(t, c.ActionDigest)

	var raw []map[string]any
	require.NoError(t, e.db.Raw("SELECT * FROM ai_tool_confirmations WHERE id = ?", c.ID).Scan(&raw).Error)
	for _, v := range raw[0] {
		assert.NotEqual(t, tok, v, "the raw token is stored nowhere")
	}

	got, res := e.lookup(tok)
	assert.Equal(t, aitools.LookupOK, res)
	assert.Equal(t, c.ID, got.ID)
}

func TestActionDigest_BindsOrganizationContactToolAndArgs(t *testing.T) {
	org, contact := uuid.New(), uuid.New()
	args := models.JSONB{"reason": "x"}
	base, err := aitools.ActionDigest("s", org, contact, "t", args)
	require.NoError(t, err)
	again, _ := aitools.ActionDigest("s", org, contact, "t", models.JSONB{"reason": "x"})
	assert.Equal(t, base, again, "deterministic")
	for name, d := range map[string]func() (string, error){
		"other org":     func() (string, error) { return aitools.ActionDigest("s", uuid.New(), contact, "t", args) },
		"other contact": func() (string, error) { return aitools.ActionDigest("s", org, uuid.New(), "t", args) },
		"other tool":    func() (string, error) { return aitools.ActionDigest("s", org, contact, "u", args) },
		"other args": func() (string, error) {
			return aitools.ActionDigest("s", org, contact, "t", models.JSONB{"reason": "y"})
		},
		"other secret": func() (string, error) { return aitools.ActionDigest("z", org, contact, "t", args) },
	} {
		got, err := d()
		require.NoError(t, err, name)
		assert.NotEqual(t, base, got, name)
	}
	_, err = aitools.ActionDigest("", org, contact, "t", args)
	assert.ErrorIs(t, err, aitools.ErrNoSecret, "no secret, no confirmation")
}

func TestConfirmation_WithoutASecretNothingCanBeProposed(t *testing.T) {
	e := newConfEnv(t, defaultParams())
	e.store.Secret = ""
	_, _, err := e.store.Propose(context.Background(), e.input(models.JSONB{}))
	assert.ErrorIs(t, err, aitools.ErrNoSecret)
	var n int64
	require.NoError(t, e.db.Model(&models.AIToolConfirmation{}).Where("organization_id = ?", e.org.ID).Count(&n).Error)
	assert.Zero(t, n)
}

func TestConfirmation_LookupIsScopedToTheOwner(t *testing.T) {
	e := newConfEnv(t, defaultParams())
	tok, _ := e.propose(t, models.JSONB{})

	otherContact, _ := e.newContact(t, e.org)
	_, res := e.store.Lookup(context.Background(), tok, e.org.ID, otherContact.ID, e.session.ID)
	assert.Equal(t, aitools.LookupNotFound, res, "another contact's tap looks like an unknown token")

	otherOrg := testutil.CreateTestOrganization(t, e.db)
	_, res = e.store.Lookup(context.Background(), tok, otherOrg.ID, e.contact.ID, e.session.ID)
	assert.Equal(t, aitools.LookupNotFound, res, "another organization")

	_, res = e.store.Lookup(context.Background(), tok, e.org.ID, e.contact.ID, uuid.New())
	assert.Equal(t, aitools.LookupNotFound, res, "a different session looks like an unknown token")
	_, res = e.store.Lookup(context.Background(), tok, e.org.ID, e.contact.ID, uuid.Nil)
	assert.Equal(t, aitools.LookupNotFound, res, "no session")

	for _, bad := range []string{"", "nope", tok + "x"} {
		_, res = e.lookup(bad)
		assert.Equal(t, aitools.LookupNotFound, res, bad)
	}

	// the session has to be the contact's
	foreignSession := &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: e.org.ID, ContactID: otherContact.ID,
		WhatsAppAccount: "acc", PhoneNumber: otherContact.PhoneNumber, Status: models.SessionStatusActive, LastActivityAt: time.Now()}
	require.NoError(t, e.db.Create(foreignSession).Error)
	in := e.input(models.JSONB{})
	in.Scope.SessionID = &foreignSession.ID
	tok2, _, err := e.store.Propose(context.Background(), in)
	require.NoError(t, err)
	_, res = e.lookup(tok2)
	assert.Equal(t, aitools.LookupNotFound, res, "a session that is not the contact's")
}

func TestConfirmation_ExpiryTamperingAndInertStates(t *testing.T) {
	e := newConfEnv(t, defaultParams())

	tok, _ := e.propose(t, models.JSONB{"reason": "a"})
	e.advance(9 * time.Minute)
	_, res := e.lookup(tok)
	assert.Equal(t, aitools.LookupOK, res)
	e.advance(2 * time.Minute)
	c, res := e.lookup(tok)
	assert.Equal(t, aitools.LookupExpired, res)
	assert.Equal(t, models.AIConfirmationPending, c.Status, "looking is not consuming")

	// tampering with the stored action invalidates it
	tok2, c2 := e.propose(t, models.JSONB{"reason": "original"})
	require.NoError(t, e.db.Model(&models.AIToolConfirmation{}).Where("id = ?", c2.ID).Update("args", models.JSONB{"reason": "altered"}).Error)
	_, res = e.lookup(tok2)
	assert.Equal(t, aitools.LookupTampered, res)

	// a state that is not pending is inert for the button
	e2 := newConfEnv(t, defaultParams())
	tok3, c3 := e2.propose(t, models.JSONB{})
	won, err := e2.store.Decline(context.Background(), c3.ID)
	require.NoError(t, err)
	require.True(t, won)
	got, res := e2.lookup(tok3)
	assert.Equal(t, aitools.LookupNotPending, res)
	assert.Equal(t, models.AIConfirmationDeclined, got.Status)
}

func TestConfirmation_TheCASIsWonByExactlyOneCaller(t *testing.T) {
	e := newConfEnv(t, defaultParams())
	_, c := e.propose(t, models.JSONB{})

	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := e.store.Confirm(context.Background(), c.ID, e.contact.ID, "wamid.tap")
			assert.NoError(t, err)
			if ok {
				atomic.AddInt32(&wins, 1)
			}
		}(i)
	}
	wg.Wait()
	assert.EqualValues(t, 1, wins, "one confirmation, however many taps")

	got, err := e.store.Get(context.Background(), c.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AIConfirmationConfirmed, got.Status)
	assert.NotNil(t, got.ConfirmedAt)
	require.NotNil(t, got.ConfirmedByContactID)
	assert.Equal(t, e.contact.ID, *got.ConfirmedByContactID)
	assert.Equal(t, "wamid.tap", got.ConfirmWAMID)
}

func TestConfirmation_AfterConfirmedNothingPendingOnlyTransitionsApply(t *testing.T) {
	e := newConfEnv(t, defaultParams())
	ctx := context.Background()
	_, c := e.propose(t, models.JSONB{})
	ok, _ := e.store.Confirm(ctx, c.ID, e.contact.ID, "w1")
	require.True(t, ok)

	// the pending-only transitions all lose now
	for name, f := range map[string]func() (bool, error){
		"confirm again": func() (bool, error) { return e.store.Confirm(ctx, c.ID, e.contact.ID, "w2") },
		"decline":       func() (bool, error) { return e.store.Decline(ctx, c.ID) },
		"expire":        func() (bool, error) { return e.store.Expire(ctx, c.ID) },
		"deny":          func() (bool, error) { return e.store.Deny(ctx, c.ID, "x") },
		"close":         func() (bool, error) { return e.store.CloseNotExecuted(ctx, c.ID, "already_active") },
	} {
		won, err := f()
		require.NoError(t, err, name)
		assert.False(t, won, name)
	}
	_, res := e.lookup("anything")
	assert.Equal(t, aitools.LookupNotFound, res)

	// only Finish moves a confirmed action, and only once
	tid := uuid.New()
	won, err := e.store.Finish(ctx, c.ID, aitools.FinishInput{Status: models.AIConfirmationExecuted, Outcome: models.AIConfirmationOutcomeCreated, TransferID: &tid})
	require.NoError(t, err)
	assert.True(t, won)
	won, _ = e.store.Finish(ctx, c.ID, aitools.FinishInput{Status: models.AIConfirmationFailed})
	assert.False(t, won, "a finished action is not finished again")
	got, _ := e.store.Get(ctx, c.ID)
	assert.Equal(t, models.AIConfirmationExecuted, got.Status)
	assert.Equal(t, models.AIConfirmationOutcomeCreated, got.Outcome)
	assert.Equal(t, tid, *got.TransferID)
	assert.NotNil(t, got.FinishedAt)

	_, err = e.store.Finish(ctx, c.ID, aitools.FinishInput{Status: models.AIConfirmationPending})
	assert.Error(t, err, "not an outcome")
}

func TestConfirmation_PendingOnlyOutcomesNeverTouchAConfirmedOrFinishedRow(t *testing.T) {
	e := newConfEnv(t, defaultParams())
	ctx := context.Background()
	_, c := e.propose(t, models.JSONB{})
	won, _ := e.store.CloseNotExecuted(ctx, c.ID, models.AIConfirmationOutcomeAlreadyActive)
	require.True(t, won)
	got, _ := e.store.Get(ctx, c.ID)
	assert.Equal(t, models.AIConfirmationNotExecuted, got.Status)
	assert.Equal(t, "already_active", got.Outcome)
	assert.Nil(t, got.ConfirmedAt, "it never passed through confirmed")
	won, _ = e.store.Confirm(ctx, c.ID, e.contact.ID, "w")
	assert.False(t, won, "a closed proposal cannot be confirmed")

	_, d := e.propose(t, models.JSONB{})
	won, _ = e.store.Deny(ctx, d.ID, "write_disabled")
	require.True(t, won)
	got, _ = e.store.Get(ctx, d.ID)
	assert.Equal(t, models.AIConfirmationDenied, got.Status)
	assert.Equal(t, "write_disabled", got.DenialReason)
	assert.Nil(t, got.ConfirmedAt)
}

func TestConfirmation_ANewProposalSupersedesThePendingOne(t *testing.T) {
	e := newConfEnv(t, defaultParams())
	tok1, c1 := e.propose(t, models.JSONB{"reason": "1"})
	tok2, _ := e.propose(t, models.JSONB{"reason": "2"})

	_, res := e.lookup(tok1)
	assert.Equal(t, aitools.LookupNotPending, res)
	got, _ := e.store.Get(context.Background(), c1.ID)
	assert.Equal(t, models.AIConfirmationSuperseded, got.Status)
	_, res = e.lookup(tok2)
	assert.Equal(t, aitools.LookupOK, res)
}

func TestConfirmation_ProposalLimitPerContactWindowAndTool(t *testing.T) {
	e := newConfEnv(t, defaultParams())
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, _, err := e.store.Propose(ctx, e.input(models.JSONB{}))
		require.NoError(t, err, "proposal %d", i+1)
	}
	_, _, err := e.store.Propose(ctx, e.input(models.JSONB{}))
	assert.ErrorIs(t, err, aitools.ErrTooManyProposals, "the fourth in the window")

	// another contact is not affected
	other, otherSession := e.newContact(t, e.org)
	in := e.input(models.JSONB{})
	in.Scope.ContactID, in.Scope.SessionID = &other.ID, &otherSession.ID
	_, _, err = e.store.Propose(ctx, in)
	assert.NoError(t, err)

	// the window moves on
	e.advance(61 * time.Minute)
	_, _, err = e.store.Propose(ctx, e.input(models.JSONB{}))
	assert.NoError(t, err)
}

func TestConfirmation_ConcurrentProposalsCannotExceedTheLimit(t *testing.T) {
	e := newConfEnv(t, defaultParams())
	var ok, tooMany int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := e.store.Propose(context.Background(), e.input(models.JSONB{}))
			switch err {
			case nil:
				atomic.AddInt32(&ok, 1)
			case aitools.ErrTooManyProposals:
				atomic.AddInt32(&tooMany, 1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 3, ok)
	assert.EqualValues(t, 7, tooMany)
}

func TestConfirmation_DeclineCooldown(t *testing.T) {
	e := newConfEnv(t, defaultParams())
	ctx := context.Background()
	_, c := e.propose(t, models.JSONB{})
	won, _ := e.store.Decline(ctx, c.ID)
	require.True(t, won)

	_, _, err := e.store.Propose(ctx, e.input(models.JSONB{}))
	assert.ErrorIs(t, err, aitools.ErrRecentlyDeclined)
	e.advance(29 * time.Minute)
	_, _, err = e.store.Propose(ctx, e.input(models.JSONB{}))
	assert.ErrorIs(t, err, aitools.ErrRecentlyDeclined)
	e.advance(2 * time.Minute)
	_, _, err = e.store.Propose(ctx, e.input(models.JSONB{}))
	assert.NoError(t, err, "the pause is over")

	// a cooldown of 0 means no pause at all
	p := defaultParams()
	p.Cooldown = 0
	e2 := newConfEnv(t, p)
	_, c2 := e2.propose(t, models.JSONB{})
	won, _ = e2.store.Decline(ctx, c2.ID)
	require.True(t, won)
	_, _, err = e2.store.Propose(ctx, e2.input(models.JSONB{}))
	assert.NoError(t, err)
}

func TestConfirmation_ReconcilerQueriesAndClaim(t *testing.T) {
	e := newConfEnv(t, defaultParams())
	ctx := context.Background()
	minAge := 60 * time.Second
	// the reconciler is global by design; the shared test database may hold other rows
	own := func(rows []models.AIToolConfirmation, err error) ([]models.AIToolConfirmation, error) {
		var mine []models.AIToolConfirmation
		for _, r := range rows {
			if r.OrganizationID == e.org.ID {
				mine = append(mine, r)
			}
		}
		return mine, err
	}

	_, c := e.propose(t, models.JSONB{})
	ok, _ := e.store.Confirm(ctx, c.ID, e.contact.ID, "w")
	require.True(t, ok)

	stuck, err := own(e.store.StuckConfirmed(ctx, minAge, 3, 10))
	require.NoError(t, err)
	assert.Empty(t, stuck, "too recent: the normal path may still be running")

	e.advance(61 * time.Second)
	stuck, _ = own(e.store.StuckConfirmed(ctx, minAge, 3, 10))
	require.Len(t, stuck, 1)

	// only one claimer wins, and the attempt is counted
	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := e.store.ClaimReconcile(ctx, c.ID, minAge)
			assert.NoError(t, err)
			if won {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, wins)
	got, _ := e.store.Get(ctx, c.ID)
	assert.Equal(t, 1, got.ReconcileAttempts)
	assert.NotNil(t, got.ReconcileClaimedAt)

	stuck, _ = own(e.store.StuckConfirmed(ctx, minAge, 3, 10))
	assert.Empty(t, stuck, "a fresh claim hides it from other reconcilers")
	e.advance(61 * time.Second)
	stuck, _ = own(e.store.StuckConfirmed(ctx, minAge, 3, 10))
	assert.Len(t, stuck, 1, "a claim that went stale can be taken again")

	// attempts run out
	for i := 0; i < 2; i++ {
		won, _ := e.store.ClaimReconcile(ctx, c.ID, minAge)
		require.True(t, won)
		e.advance(61 * time.Second)
	}
	stuck, _ = own(e.store.StuckConfirmed(ctx, minAge, 3, 10))
	assert.Empty(t, stuck, "3 attempts used")
	ex, _ := own(e.store.ExhaustedConfirmed(ctx, minAge, 3, 10))
	assert.Len(t, ex, 1)

	// a finished confirmation is never offered
	won, _ := e.store.Finish(ctx, c.ID, aitools.FinishInput{Status: models.AIConfirmationFailed, ErrorKind: models.AIConfirmationErrReconcileExhausted})
	require.True(t, won)
	ex, _ = own(e.store.ExhaustedConfirmed(ctx, minAge, 3, 10))
	assert.Empty(t, ex)
	won, _ = e.store.ClaimReconcile(ctx, c.ID, minAge)
	assert.False(t, won)
}

func TestConfirmation_SetExecutionCall(t *testing.T) {
	e := newConfEnv(t, defaultParams())
	_, c := e.propose(t, models.JSONB{})
	call := uuid.New()
	require.NoError(t, e.store.SetExecutionCall(context.Background(), c.ID, call))
	got, _ := e.store.Get(context.Background(), c.ID)
	require.NotNil(t, got.ExecutionCallID)
	assert.Equal(t, call, *got.ExecutionCallID)
}
