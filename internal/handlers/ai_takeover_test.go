package handlers

import (
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// --- shared wiring fixture (also used by the handoff and end-to-end tests) -----------------

// waCounter is a fake WhatsApp Cloud API that records what the app sends.
type waCounter struct {
	mu     sync.Mutex
	bodies []string
}

func (w *waCounter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.bodies)
}

func (w *waCounter) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.bodies...)
}

// wiring is an app whose WhatsApp API and AI provider are fakes, with an organization, a unit, a
// team, a number allowed for the agent and the agent itself, ready for conversations.
type wiring struct {
	app *App
	f   aiAgentFixture
	wa  *waCounter
	ai  *seqTransport
}

func newWiring(t *testing.T, replies ...string) *wiring {
	t.Helper()
	db := testutil.SetupTestDB(t)
	log := testutil.NopLogger()

	wa := &waCounter{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		wa.mu.Lock()
		wa.bodies = append(wa.bodies, string(body))
		wa.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{{"id": "wamid.mock_" + uuid.NewString()[:8]}}})
	}))
	t.Cleanup(srv.Close)

	tr := &seqTransport{replies: replies}
	app := &App{
		Config:     &config.Config{App: config.AppConfig{EncryptionKey: "test-encryption-key-for-handlers-longer-than-32-chars"}},
		DB:         db,
		Log:        log,
		WhatsApp:   whatsapp.NewWithBaseURL(log, srv.URL),
		HTTPClient: &http.Client{Transport: tr, Timeout: 5 * time.Second},
	}
	rdb := testutil.SetupTestRedis(t)
	require.NotNil(t, rdb, "TEST_REDIS_URL must be set")
	app.Redis = rdb

	f := newAIAgentFixture(t, app)
	enc, err := crypto.Encrypt("sk-test", app.Config.App.EncryptionKey)
	require.NoError(t, err)
	f.settings.IsEnabled = true
	f.settings.FallbackMessage = "Não entendi, pode repetir?"
	f.settings.AI = models.AIConfig{
		Enabled: true, Provider: models.AIProvider("openai"), Model: "gpt-test", APIKey: enc, MaxTokens: 200,
		DefaultAgentID: &f.agent.ID,
	}
	require.NoError(t, db.Create(f.settings).Error)
	require.NoError(t, db.Model(f.agent).Updates(map[string]any{
		"objective": "Qualificar compras de pisos", "company_info": "Atacadão dos Pisos, loja Centro",
	}).Error)
	return &wiring{app: app, f: f, wa: wa, ai: tr}
}

// inbound delivers one customer text message to the processor, as the webhook would.
func (w *wiring) inbound(t *testing.T, from, text string) {
	t.Helper()
	var msg IncomingTextMessage
	raw := `{"from":"` + from + `","id":"wamid.` + uuid.NewString()[:8] + `","type":"text","text":{"body":` + mustJSON(text) + `}}`
	require.NoError(t, json.Unmarshal([]byte(raw), &msg))
	w.app.processIncomingMessageFull(w.f.account.PhoneID, msg, "Cliente")
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// contactFor returns the contact the processor created for from.
func (w *wiring) contactFor(t *testing.T, from string) *models.Contact {
	t.Helper()
	var c models.Contact
	require.NoError(t, w.app.DB.Where("organization_id = ? AND phone_number = ?", w.f.org.ID, from).First(&c).Error)
	return &c
}

// openAttendance makes a human own the contact, the way an attendant opening the conversation does.
func (w *wiring) openAttendance(t *testing.T, contactID uuid.UUID) {
	t.Helper()
	agent := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID)
	tr := models.AgentTransfer{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID, ContactID: contactID,
		WhatsAppAccount: w.f.account.Name, PhoneNumber: "x", Status: models.TransferStatusActive,
		Source: models.TransferSourceAgentInitiated, AgentID: &agent.ID, TransferredAt: time.Now(),
	}
	require.NoError(t, w.app.createTransferRow(&tr))
}

// --- humanTookOver ---------------------------------------------------------------------------

func TestHumanTookOver(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	assert.False(t, app.humanTookOver(org.ID, contact.ID))

	user := testutil.CreateTestUser(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", user.ID).Error)
	assert.False(t, app.humanTookOver(org.ID, contact.ID), "an assigned owner (carteira) is not a takeover")

	tr := models.AgentTransfer{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
		PhoneNumber: contact.PhoneNumber, Status: models.TransferStatusActive, Source: models.TransferSourceManual, TransferredAt: time.Now()}
	require.NoError(t, app.createTransferRow(&tr))
	assert.True(t, app.humanTookOver(org.ID, contact.ID))
	assert.False(t, app.humanTookOver(uuid.New(), contact.ID), "another organization does not see it")
}

// --- the lock -----------------------------------------------------------------------------------

func discardedRows(t *testing.T, app *App, orgID, contactID uuid.UUID) []models.AIUsageLog {
	t.Helper()
	var rows []models.AIUsageLog
	require.NoError(t, app.DB.Where("organization_id = ? AND contact_id = ? AND feature = ?", orgID, contactID, aiFeatureReplyDiscarded).Find(&rows).Error)
	return rows
}

func newTakeoverTarget(t *testing.T, app *App) (*models.Organization, *models.Contact) {
	t.Helper()
	org := testutil.CreateTestOrganization(t, app.DB)
	return org, testutil.CreateTestContact(t, app.DB, org.ID)
}

func attendance(org *models.Organization, contact *models.Contact) *models.AgentTransfer {
	return &models.AgentTransfer{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
		PhoneNumber: contact.PhoneNumber, Status: models.TransferStatusActive, Source: models.TransferSourceAgentInitiated, TransferredAt: time.Now()}
}

// The attendance was confirmed before the AI's check: the AI sends nothing.
func TestSendAIReply_HumanCommitsBeforeCheck_NothingSent(t *testing.T) {
	app := newSLATestApp(t)
	org, contact := newTakeoverTarget(t, app)
	require.NoError(t, app.createTransferRow(attendance(org, contact)))

	called := false
	sent, err := app.sendAIReplyIfNoHuman(context.Background(), org.ID, contact.ID, func(context.Context) error {
		called = true
		return nil
	})
	require.NoError(t, err)
	assert.False(t, sent)
	assert.False(t, called, "the send must not even start")
	rows := discardedRows(t, app, org.ID, contact.ID)
	require.Len(t, rows, 1)
	assert.Equal(t, aiDiscardHumanTookOver, rows[0].ErrorKind)
	assert.Zero(t, rows[0].TotalTokens, "a discard never counts as consumption")
}

// The AI's send is in flight: the attendant's confirmation waits for it, so the AI message is
// strictly earlier than the attendance.
func TestSendAIReply_HumanWaitsForInFlightSend(t *testing.T) {
	app := newSLATestApp(t)
	org, contact := newTakeoverTarget(t, app)

	started, release := make(chan struct{}), make(chan struct{})
	var seq atomic.Int64
	var sendSeq, transferSeq int64

	aiDone := make(chan bool, 1)
	go func() {
		sent, err := app.sendAIReplyIfNoHuman(context.Background(), org.ID, contact.ID, func(context.Context) error {
			close(started)
			<-release
			sendSeq = seq.Add(1)
			return nil
		})
		assert.NoError(t, err)
		aiDone <- sent
	}()
	<-started

	humanDone := make(chan struct{})
	go func() {
		assert.NoError(t, app.createTransferRow(attendance(org, contact)))
		transferSeq = seq.Add(1)
		close(humanDone)
	}()

	select {
	case <-humanDone:
		t.Fatal("the attendance was confirmed while an AI message was still being sent")
	case <-time.After(400 * time.Millisecond):
	}
	close(release)
	require.True(t, <-aiDone)
	select {
	case <-humanDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the attendance never completed")
	}
	assert.Less(t, sendSeq, transferSeq, "no AI message after the attendance is confirmed")
	assert.True(t, app.humanTookOver(org.ID, contact.ID))
}

func TestSendAIReply_LockTimeoutDiscards(t *testing.T) {
	old := aiSendLockTimeout
	aiSendLockTimeout = 300 * time.Millisecond
	t.Cleanup(func() { aiSendLockTimeout = old })

	app := newSLATestApp(t)
	org, contact := newTakeoverTarget(t, app)

	held, free := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = app.DB.Transaction(func(tx *gorm.DB) error {
			assert.NoError(t, lockContact(tx, org.ID, contact.ID))
			close(held)
			<-free
			return nil
		})
	}()
	<-held

	called := false
	sent, err := app.sendAIReplyIfNoHuman(context.Background(), org.ID, contact.ID, func(context.Context) error {
		called = true
		return nil
	})
	close(free)
	<-done
	require.NoError(t, err)
	assert.False(t, sent)
	assert.False(t, called, "without the lock nothing is sent")
	rows := discardedRows(t, app, org.ID, contact.ID)
	require.Len(t, rows, 1)
	assert.Equal(t, aiDiscardLockTimeout, rows[0].ErrorKind)
}

func TestCreateTransferRow_TakesContactLock(t *testing.T) {
	app := newSLATestApp(t)
	org, contact := newTakeoverTarget(t, app)

	held, free, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		_ = app.DB.Transaction(func(tx *gorm.DB) error {
			require.NoError(t, lockContact(tx, org.ID, contact.ID))
			close(held)
			<-free
			return nil
		})
	}()
	<-held
	go func() {
		assert.NoError(t, app.createTransferRow(attendance(org, contact)))
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("createTransferRow ignored the contact lock")
	case <-time.After(300 * time.Millisecond):
	}
	close(free)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("createTransferRow never finished after the lock was freed")
	}
}

// Many interleavings of "the AI sends" and "an attendant confirms the attendance". The invariant:
// whenever the AI's message went out, it went out before the attendance was confirmed.
func TestSendAIReply_Race_NoAgentMessageAfterTransfer(t *testing.T) {
	app := newSLATestApp(t)
	const rounds = 120
	violations, sentCount := 0, 0
	for i := 0; i < rounds; i++ {
		org, contact := newTakeoverTarget(t, app)
		var seq atomic.Int64
		var sendSeq, transferSeq int64
		var sent bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(rand.Intn(3)) * time.Millisecond)
			s, err := app.sendAIReplyIfNoHuman(context.Background(), org.ID, contact.ID, func(context.Context) error {
				time.Sleep(time.Duration(rand.Intn(4)) * time.Millisecond) // the window a bare check would leave open
				sendSeq = seq.Add(1)
				return nil
			})
			assert.NoError(t, err)
			sent = s
		}()
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(rand.Intn(3)) * time.Millisecond)
			assert.NoError(t, app.createTransferRow(attendance(org, contact)))
			transferSeq = seq.Add(1)
		}()
		wg.Wait()
		if sent {
			sentCount++
			if sendSeq > transferSeq {
				violations++
			}
		}
		assert.True(t, app.humanTookOver(org.ID, contact.ID))
	}
	assert.Zero(t, violations, "an AI message left after the attendance was confirmed")
	t.Logf("%d of %d rounds had the AI win the race; the rest were discarded", sentCount, rounds)
}

// --- wiring: both AI send paths use the guard -----------------------------------------------

// The model is "thinking" and a human takes over: the direct-reply path sends nothing.
func TestProcessor_AgentReplyDropped_WhenHumanTakesOverMidCall(t *testing.T) {
	w := newWiring(t, "Qual ambiente você quer revestir?")
	const from = "5511900000001"
	w.ai.onCall = func(int) { w.openAttendance(t, w.contactFor(t, from).ID) }

	w.inbound(t, from, "oi, quero porcelanato")

	assert.Equal(t, 0, w.wa.count(), "no WhatsApp message may go out")
	contact := w.contactFor(t, from)
	assert.Len(t, discardedRows(t, w.app, w.f.org.ID, contact.ID), 1)
	assert.True(t, w.app.humanTookOver(w.f.org.ID, contact.ID))
}

// Control: with nobody taking over, the same agent reply does reach the customer.
func TestProcessor_AgentReplySent_WhenNobodyTakesOver(t *testing.T) {
	w := newWiring(t, "Qual ambiente você quer revestir?")
	w.inbound(t, "5511900000002", "oi, quero porcelanato")
	require.Equal(t, 1, w.wa.count())
	assert.Contains(t, w.wa.all()[0], "Qual ambiente")
}

// And once a human owns the conversation, later customer messages do not reach the AI at all.
func TestProcessor_AgentSilent_AfterTakeover(t *testing.T) {
	w := newWiring(t, "resposta que não deve existir")
	const from = "5511900000003"
	w.inbound(t, from, "primeira mensagem") // creates the contact; the agent answers
	before := w.wa.count()
	calls := len(w.ai.bodies)

	w.openAttendance(t, w.contactFor(t, from).ID)
	w.inbound(t, from, "e o prazo?")

	assert.Equal(t, before, w.wa.count(), "nothing sent after the takeover")
	assert.Equal(t, calls, len(w.ai.bodies), "the model is not even called")
}

// The ai_response node of a flow uses the same guard.
func TestGraphNode_AgentReplyDropped_WhenHumanTakesOverMidCall(t *testing.T) {
	w := newWiring(t, "Qual ambiente você quer revestir?")
	contact := testutil.CreateTestContact(t, w.app.DB, w.f.org.ID)
	session := &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID, ContactID: contact.ID,
		WhatsAppAccount: w.f.account.Name, PhoneNumber: contact.PhoneNumber, Status: models.SessionStatusActive,
		SessionData: models.JSONB{}, StartedAt: time.Now(), LastActivityAt: time.Now()}
	require.NoError(t, w.app.DB.Create(session).Error)
	flow := newAIResponseFlow(t, w.app, w.f.org, w.f.account, "")
	w.ai.onCall = func(int) { w.openAttendance(t, contact.ID) }

	require.NoError(t, w.app.runChatGraph(w.f.account, contact, session, flow, "quero porcelanato", "", nil))

	assert.Equal(t, 0, w.wa.count(), "the node must stay silent")
	assert.Len(t, discardedRows(t, w.app, w.f.org.ID, contact.ID), 1)
}

func TestGraphNode_AgentReplySent_WhenNobodyTakesOver(t *testing.T) {
	w := newWiring(t, "Qual ambiente você quer revestir?")
	contact := testutil.CreateTestContact(t, w.app.DB, w.f.org.ID)
	session := &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID, ContactID: contact.ID,
		WhatsAppAccount: w.f.account.Name, PhoneNumber: contact.PhoneNumber, Status: models.SessionStatusActive,
		SessionData: models.JSONB{}, StartedAt: time.Now(), LastActivityAt: time.Now()}
	require.NoError(t, w.app.DB.Create(session).Error)
	flow := newAIResponseFlow(t, w.app, w.f.org, w.f.account, "")

	require.NoError(t, w.app.runChatGraph(w.f.account, contact, session, flow, "quero porcelanato", "", nil))
	assert.Equal(t, 1, w.wa.count())
}

// --- structural guard ---------------------------------------------------------------------------

// Any other INSERT of an active AgentTransfer would bypass the contact lock and reopen the race.
func TestNoOtherActiveTransferInsertPath(t *testing.T) {
	literal := regexp.MustCompile(`models\.AgentTransfer\{[^}\n]`)
	// inserts only: Save/Update on a transfer that is already active (queue pick, claim, assign)
	// changes who owns an attendance that already silences the AI
	insert := regexp.MustCompile(`(Create|FirstOrCreate)\(&?(transfer|t|tr|agentTransfer|newTransfer)\b|Create\(&models\.AgentTransfer\{`)
	root := filepath.Join("..", "..", "internal")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		base := filepath.Base(path)
		if base != "agent_transfers.go" && literal.Match(src) {
			t.Errorf("%s builds an AgentTransfer; create transfers through createTransferRow's callers in agent_transfers.go", path)
		}
		if base != "distribution.go" && strings.Contains(string(src), "AgentTransfer") && insert.Match(src) &&
			!strings.Contains(path, filepath.Join("internal", "calling")) {
			t.Errorf("%s inserts a row outside createTransferRow", path)
		}
		return nil
	})
	require.NoError(t, err)
}

// --- review follow-ups -------------------------------------------------------------------------

// After a dropped ai_response node the flow must STOP: no later node may speak, and the session
// that the attendance ended must not be saved back as active.
func TestGraphNode_DroppedReply_StopsFlowAndKeepsSessionEnded(t *testing.T) {
	w := newWiring(t, "Qual ambiente você quer revestir?")
	contact := testutil.CreateTestContact(t, w.app.DB, w.f.org.ID)
	session := &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID, ContactID: contact.ID,
		WhatsAppAccount: w.f.account.Name, PhoneNumber: contact.PhoneNumber, Status: models.SessionStatusActive,
		SessionData: models.JSONB{}, StartedAt: time.Now(), LastActivityAt: time.Now()}
	require.NoError(t, w.app.DB.Create(session).Error)
	flow := &models.ChatbotFlow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID,
		WhatsAppAccount: w.f.account.Name, Name: "ai-then-message", IsEnabled: true,
		Graph: models.JSONB{"version": 2, "entry_node": "ai",
			"nodes": []any{
				map[string]any{"id": "ai", "type": "ai_response", "label": "ask", "config": map[string]any{}},
				map[string]any{"id": "after", "type": "message", "label": "after", "config": map[string]any{"text": "MENSAGEM POSTERIOR DO FLUXO"}},
				map[string]any{"id": "end", "type": "end"},
			},
			"edges": []any{
				map[string]any{"from": "ai", "to": "after", "condition": "default"},
				map[string]any{"from": "after", "to": "end", "condition": "default"},
			}}}
	require.NoError(t, w.app.DB.Create(flow).Error)

	w.ai.onCall = func(int) {
		agent := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID)
		// the real takeover: opens the attendance AND ends the chatbot session
		require.NoError(t, w.app.openAgentInitiatedTransfer(w.f.account, contact, agent.ID))
	}
	require.NoError(t, w.app.runChatGraph(w.f.account, contact, session, flow, "quero porcelanato", "", nil))

	assert.Equal(t, 0, w.wa.count(), "neither the AI reply nor the next node may be sent")
	var stored models.ChatbotSession
	require.NoError(t, w.app.DB.First(&stored, "id = ?", session.ID).Error)
	assert.NotEqual(t, models.SessionStatusActive, stored.Status, "the session the attendance ended must stay ended")
}

// The fallback after an agent turn is part of that turn: a human who took over while the model was
// working must not be followed by a "did not understand" message.
func TestProcessor_FallbackAfterAgentTurn_NotSentWhenHumanTookOver(t *testing.T) {
	w := newWiring(t, "ok", "") // second answer is empty -> the processor falls to the fallback message
	const from = "5511900000004"
	w.inbound(t, from, "primeira") // new session; the agent answers "ok"
	before := w.wa.count()
	require.Equal(t, 1, before)

	w.ai.onCall = func(i int) {
		if i == 1 {
			w.openAttendance(t, w.contactFor(t, from).ID)
		}
	}
	w.inbound(t, from, "segunda")

	assert.Equal(t, before, w.wa.count(), "the fallback must not be sent over a human")
}

// Control: with nobody taking over, an empty agent answer does fall to the fallback message.
func TestProcessor_FallbackAfterAgentTurn_SentWhenNobodyTookOver(t *testing.T) {
	w := newWiring(t, "ok", "")
	const from = "5511900000005"
	w.inbound(t, from, "primeira")
	w.inbound(t, from, "segunda")
	require.Equal(t, 2, w.wa.count())
	assert.Contains(t, w.wa.all()[1], "Não entendi")
}

// More concurrent AI sends than the connection pool can serve must not freeze the database: each
// send holds a connection while its callback needs another.
func TestSendAIReply_ManyConcurrentSendsDoNotExhaustThePool(t *testing.T) {
	app := newSLATestApp(t)
	sqlDB, err := app.DB.DB()
	require.NoError(t, err)
	oldMax := sqlDB.Stats().MaxOpenConnections
	sqlDB.SetMaxOpenConns(8)
	aiSendSlotsOnce, aiSendSlots = sync.Once{}, nil // size the semaphore for this small pool
	t.Cleanup(func() {
		sqlDB.SetMaxOpenConns(oldMax)
		aiSendSlotsOnce, aiSendSlots = sync.Once{}, nil
	})
	old := aiSendLockTimeout
	aiSendLockTimeout = 2 * time.Second
	t.Cleanup(func() { aiSendLockTimeout = old })

	org := testutil.CreateTestOrganization(t, app.DB)
	var done sync.WaitGroup
	var sent, discarded atomic.Int64
	for i := 0; i < 40; i++ {
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		done.Add(1)
		go func() {
			defer done.Done()
			ok, err := app.sendAIReplyIfNoHuman(context.Background(), org.ID, contact.ID, func(ctx context.Context) error {
				// what a real send does: more database work on other connections
				var n int64
				if err := app.DB.WithContext(ctx).Model(&models.Contact{}).Count(&n).Error; err != nil {
					return err
				}
				time.Sleep(30 * time.Millisecond)
				return nil
			})
			assert.NoError(t, err)
			if ok {
				sent.Add(1)
			} else {
				discarded.Add(1)
			}
		}()
	}
	finished := make(chan struct{})
	go func() { done.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock: AI sends exhausted the connection pool")
	}
	assert.EqualValues(t, 40, sent.Load()+discarded.Load())
	assert.Greater(t, sent.Load(), int64(0))
	// and an attendant can still take over afterwards
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.createTransferRow(attendance(&models.Organization{BaseModel: models.BaseModel{ID: org.ID}}, contact)))
}
