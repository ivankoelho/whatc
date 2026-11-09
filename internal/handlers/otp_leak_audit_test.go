package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/authmask"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/internal/worker"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"github.com/zerodha/logf"
)

// This audit follows ONE synthetic code through the real import, start, worker, retry,
// recovery and release code, with failures and interruptions in the middle, and after
// every step looks for it in every place it could leak: every table, every Redis key
// and pub/sub message, every log line, the jobs as they are queued, and the responses of
// the APIs that read campaigns, recipients, messages, contacts and the audit log. The
// Meta mock also echoes the code in its error answers, to exercise the redaction.

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type leakAudit struct {
	*cycleEnv
	code     string
	logs     *syncBuf
	pubsub   *syncBuf
	reader   *models.User // a super admin: the widest read access there is
	startXID int64        // rows written before this test started cannot hold its code: only newer ones are cast to text
}

func newLeakAudit(t *testing.T) *leakAudit {
	t.Helper()
	e := newCycleEnv(t)
	a := &leakAudit{cycleEnv: e, logs: &syncBuf{}, pubsub: &syncBuf{}}
	require.NoError(t, e.app.DB.Raw("SELECT txid_current() % 4294967296").Scan(&a.startXID).Error)
	a.code = fmt.Sprintf("7%08d", time.Now().UnixNano()%100000000) // unique per run: no other test shares it

	// Every log line of the handlers, the worker and the Meta client is captured.
	log := logf.New(logf.Opts{Writer: a.logs, Level: logf.DebugLevel, EnableColor: false})
	e.app.Log = log
	e.w.Log = log
	e.w.WhatsApp = whatsapp.NewWithBaseURL(log, e.srv.URL)

	// Every pub/sub message (the source of the websocket events) is captured too.
	if rdb := e.app.Redis; rdb != nil {
		sub := rdb.PSubscribe(context.Background(), "*")
		_, err := sub.Receive(context.Background())
		require.NoError(t, err)
		t.Cleanup(func() { _ = sub.Close() })
		go func() {
			for m := range sub.Channel() {
				_, _ = a.pubsub.Write([]byte(m.Channel + " " + m.Payload + "\n"))
			}
		}()
	}
	a.reader = testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithSuperAdmin(), testutil.WithEmail(testutil.UniqueEmail("leak-reader")))
	return a
}

// importCode imports one recipient carrying the synthetic code through the real handler.
func (a *leakAudit) importCode(t *testing.T) {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{
		"recipients": []map[string]any{{"phone_number": "+5511999990000", "template_params": map[string]any{"1": a.code}}},
	})
	testutil.SetAuthContext(req, a.org.ID, a.user.ID)
	testutil.SetPathParam(req, "id", a.campaign.ID.String())
	require.NoError(t, a.app.ImportRecipients(req))
	require.Equal(t, fasthttp.StatusOK, req.RequestCtx.Response.StatusCode(), "%s", req.RequestCtx.Response.Body())
}

func (a *leakAudit) start(t *testing.T) {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, a.org.ID, a.user.ID)
	testutil.SetPathParam(req, "id", a.campaign.ID.String())
	require.NoError(t, a.app.StartCampaign(req))
	require.Equal(t, fasthttp.StatusOK, req.RequestCtx.Response.StatusCode(), "%s", req.RequestCtx.Response.Body())
}

// metaEchoes makes the mock answer with the code inside the error text, as a hostile or
// careless upstream could.
func (a *leakAudit) metaEchoes(status int) {
	a.metaAnswers(status, fmt.Sprintf(`{"error":{"code":131008,"message":"Invalid OTP %s for this template","error_user_msg":"code=%s","error_data":{"details":"param 1 = %s"}}}`, a.code, a.code, a.code))
}

func (a *leakAudit) allTablesContain(t *testing.T) []string {
	t.Helper()
	var tables []string
	require.NoError(t, a.app.DB.Raw(`SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`).Scan(&tables).Error)
	var (
		mu   sync.Mutex
		hits []string
		wg   sync.WaitGroup
		sem  = make(chan struct{}, 8) // the shared test database is large: scan tables in parallel
	)
	for _, tb := range tables {
		wg.Add(1)
		sem <- struct{}{}
		go func(tb string) {
			defer wg.Done()
			defer func() { <-sem }()
			var n int64
			if err := a.app.DB.Raw(`SELECT count(*) FROM "`+tb+`" x WHERE x.xmin::text::bigint >= ? AND x::text LIKE ?`, a.startXID, "%"+a.code+"%").Scan(&n).Error; err != nil {
				mu.Lock()
				hits = append(hits, fmt.Sprintf("table %s could not be scanned: %v", tb, err))
				mu.Unlock()
				return
			}
			if n > 0 {
				mu.Lock()
				hits = append(hits, fmt.Sprintf("table %s (%d rows)", tb, n))
				mu.Unlock()
			}
		}(tb)
	}
	wg.Wait()
	return hits
}

func (a *leakAudit) redisContains(t *testing.T) []string {
	t.Helper()
	rdb := a.app.Redis
	if rdb == nil {
		return nil
	}
	ctx := context.Background()
	var hits []string
	var cursor uint64
	for {
		keys, next, err := rdb.Scan(ctx, cursor, "*", 1000).Result()
		require.NoError(t, err)

		// The test Redis can hold thousands of keys: ask for their types, then their
		// contents, a page at a time in two pipelines instead of one round trip per key.
		types := make([]*redis.StatusCmd, len(keys))
		pipe := rdb.Pipeline()
		for i, k := range keys {
			types[i] = pipe.Type(ctx, k)
		}
		_, _ = pipe.Exec(ctx)

		dumps := make([]func() string, len(keys))
		pipe = rdb.Pipeline()
		for i, k := range keys {
			switch types[i].Val() {
			case "string":
				c := pipe.Get(ctx, k)
				dumps[i] = func() string { return c.Val() }
			case "list":
				c := pipe.LRange(ctx, k, 0, -1)
				dumps[i] = func() string { return fmt.Sprint(c.Val()) }
			case "set":
				c := pipe.SMembers(ctx, k)
				dumps[i] = func() string { return fmt.Sprint(c.Val()) }
			case "zset":
				c := pipe.ZRange(ctx, k, 0, -1)
				dumps[i] = func() string { return fmt.Sprint(c.Val()) }
			case "hash":
				c := pipe.HGetAll(ctx, k)
				dumps[i] = func() string { return fmt.Sprint(c.Val()) }
			case "stream":
				c := pipe.XRange(ctx, k, "-", "+")
				dumps[i] = func() string { return fmt.Sprint(c.Val()) }
			}
		}
		_, _ = pipe.Exec(ctx)
		for i, k := range keys {
			dump := ""
			if dumps[i] != nil {
				dump = dumps[i]()
			}
			if strings.Contains(dump, a.code) || strings.Contains(k, a.code) {
				hits = append(hits, "redis key "+k)
			}
		}
		if next == 0 {
			break
		}
		cursor = next
	}
	return hits
}

func (a *leakAudit) apiBody(t *testing.T, h func(*fastglue.Request) error, path map[string]string, query string) string {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, a.org.ID, a.reader.ID)
	for k, v := range path {
		testutil.SetPathParam(req, k, v)
	}
	if query != "" {
		req.RequestCtx.QueryArgs().Parse(query)
	}
	require.NoError(t, h(req))
	return string(req.RequestCtx.Response.Body())
}

func (a *leakAudit) apiContains(t *testing.T) []string {
	t.Helper()
	var contactID string
	var c models.Contact
	if err := a.app.DB.Where("organization_id = ? AND phone_number LIKE ?", a.org.ID, "%999990000").First(&c).Error; err == nil {
		contactID = c.ID.String()
	}
	cid := a.campaign.ID.String()
	surfaces := map[string]string{
		"GET /campaigns":            a.apiBody(t, a.app.ListCampaigns, nil, ""),
		"GET /campaigns/{id}":       a.apiBody(t, a.app.GetCampaign, map[string]string{"id": cid}, ""),
		"GET /campaigns/{id}/recip": a.apiBody(t, a.app.GetCampaignRecipients, map[string]string{"id": cid}, ""),
		"GET /contacts":             a.apiBody(t, a.app.ListContacts, nil, ""),
		"GET /audit-logs":           a.apiBody(t, a.app.ListAuditLogs, nil, ""),
		"GET /analytics/messages":   a.apiBody(t, a.app.GetMessageAnalytics, nil, ""),
	}
	if contactID != "" {
		surfaces["GET /contacts/{id}"] = a.apiBody(t, a.app.GetContact, map[string]string{"id": contactID}, "")
		surfaces["GET /contacts/{id}/messages"] = a.apiBody(t, a.app.GetMessages, map[string]string{"id": contactID}, "")
	}
	var hits []string
	for name, body := range surfaces {
		if strings.Contains(body, a.code) {
			hits = append(hits, "API "+name)
		}
	}
	return hits
}

// assertNoLeak looks everywhere. stage names the step, for the failure message.
func (a *leakAudit) findLeaks(t *testing.T) []string {
	t.Helper()
	time.Sleep(60 * time.Millisecond) // let pub/sub messages and async log lines land
	var hits []string
	hits = append(hits, a.allTablesContain(t)...)
	hits = append(hits, a.redisContains(t)...)
	hits = append(hits, a.apiContains(t)...)
	if strings.Contains(a.logs.String(), a.code) {
		hits = append(hits, "log output")
	}
	if strings.Contains(a.pubsub.String(), a.code) {
		hits = append(hits, "redis pub/sub (websocket events)")
	}
	for i, j := range a.queue.Jobs {
		raw, _ := json.Marshal(j)
		if strings.Contains(string(raw), a.code) {
			hits = append(hits, fmt.Sprintf("queued job %d", i))
		}
	}
	jobRaw, _ := json.Marshal(a.job(t))
	if strings.Contains(string(jobRaw), a.code) {
		hits = append(hits, "job built from the recipient")
	}
	return hits
}

// ---- scenarios ----

func TestOTPLeakAudit_ImportAndStart(t *testing.T) {
	a := newLeakAudit(t)
	a.importCode(t)
	a.assertNoLeak(t, "after import")
	a.start(t)
	a.assertNoLeak(t, "after start (jobs queued)")
}

func TestOTPLeakAudit_SendSucceedsAndTheClaimIsInFlight(t *testing.T) {
	a := newLeakAudit(t)
	a.importCode(t)
	a.start(t)
	job := a.queue.Jobs[0]
	a.arrived = make(chan struct{}, 1)
	a.hold = make(chan struct{})

	done := make(chan struct{})
	go func() { defer close(done); _ = a.w.HandleRecipientJob(context.Background(), job) }()
	<-a.arrived
	a.assertNoLeak(t, "claim made, request in flight (pending masked message)")

	close(a.hold)
	<-done
	assert.Equal(t, models.MessageStatusSent, a.recipient(t).Status)
	a.assertNoLeak(t, "sent")
}

func TestOTPLeakAudit_MetaRefusesAndEchoesTheCode(t *testing.T) {
	a := newLeakAudit(t)
	a.importCode(t)
	a.start(t)
	a.metaEchoes(400)

	require.NoError(t, a.w.HandleRecipientJob(context.Background(), a.queue.Jobs[0]))

	got := a.recipient(t)
	require.Equal(t, models.MessageStatusFailed, got.Status)
	assert.Contains(t, got.ErrorMessage, "meta_refused", "the failure is recorded, without the code")
	a.assertNoLeak(t, "refused with the code echoed in Meta's error")
}

func TestOTPLeakAudit_MetaFailsAndEchoesTheCode_5xx_Uncertain(t *testing.T) {
	a := newLeakAudit(t)
	a.importCode(t)
	a.start(t)
	a.metaEchoes(502)

	require.NoError(t, a.w.HandleRecipientJob(context.Background(), a.queue.Jobs[0]))

	got := a.recipient(t)
	require.NotNil(t, got.MessageID)
	assert.Contains(t, got.ErrorMessage, worker.ReasonDeliveryUncertain)
	a.assertNoLeak(t, "uncertain (5xx) with the code echoed")
}

func TestOTPLeakAudit_MetaAnswersGarbageContainingTheCode(t *testing.T) {
	a := newLeakAudit(t)
	a.importCode(t)
	a.start(t)
	a.metaAnswers(200, "not json: "+a.code)

	require.NoError(t, a.w.HandleRecipientJob(context.Background(), a.queue.Jobs[0]))

	assert.Contains(t, a.recipient(t).ErrorMessage, worker.ReasonDeliveryUncertain)
	a.assertNoLeak(t, "unreadable answer containing the code")
}

func TestOTPLeakAudit_TimeoutLeavesAnUncertainClaim(t *testing.T) {
	a := newLeakAudit(t)
	a.importCode(t)
	a.start(t)
	a.delay.Store(int64(1500 * time.Millisecond))
	a.w.WhatsApp.HTTPClient.Timeout = 200 * time.Millisecond

	require.NoError(t, a.w.HandleRecipientJob(context.Background(), a.queue.Jobs[0]))

	assert.Contains(t, a.recipient(t).ErrorMessage, worker.ReasonDeliveryUncertain)
	a.assertNoLeak(t, "timeout (request never answered)")
}

func TestOTPLeakAudit_ACrashedClaimIsRecoveredAndRedelivered(t *testing.T) {
	a := newLeakAudit(t)
	a.importCode(t)
	a.start(t)
	// A worker that claimed (masked pending message) and died: nothing else happened.
	var tpl models.Template
	require.NoError(t, a.app.DB.First(&tpl, a.campaign.TemplateID).Error)
	r := a.recipient(t)
	contact := testutil.CreateTestContactWith(t, a.app.DB, a.org.ID, testutil.WithPhoneNumber(r.PhoneNumber))
	msg := &models.Message{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: a.org.ID, ContactID: contact.ID,
		Direction: models.DirectionOutgoing, MessageType: models.MessageTypeTemplate, Status: models.MessageStatusPending,
		Content:  templateutil.MaskAllParams(tpl.BodyContent),
		Metadata: models.JSONB{"campaign_id": a.campaign.ID.String(), "auth_code_masked": true},
	}
	require.NoError(t, a.app.DB.Create(msg).Error)
	require.NoError(t, a.app.DB.Exec("UPDATE messages SET created_at = ? WHERE id = ?", time.Now().Add(-10*time.Minute), msg.ID).Error)
	require.NoError(t, a.app.DB.Model(&models.BulkMessageRecipient{}).Where("id = ?", r.ID).UpdateColumn("message_id", msg.ID).Error)
	a.assertNoLeak(t, "crashed claim, before recovery")

	n, err := a.w.ResolveStaleClaims(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)
	require.NoError(t, a.w.HandleRecipientJob(context.Background(), a.queue.Jobs[0])) // Redis redelivers it
	assert.Zero(t, a.calls.Load(), "an unresolved claim is never retried automatically")
	a.assertNoLeak(t, "claim resolved as uncertain, job redelivered")
}

func TestOTPLeakAudit_ShutdownBeforeTheRequestUndoesTheClaim(t *testing.T) {
	a := newLeakAudit(t)
	a.importCode(t)
	a.start(t)
	a.w.BeginShutdown()

	err := a.w.HandleRecipientJob(context.Background(), a.queue.Jobs[0])

	require.Error(t, err, "the job is not acknowledged")
	assert.Zero(t, a.calls.Load())
	a.assertNoLeak(t, "worker stopping")
}

func TestOTPLeakAudit_RefusedThenRetriedThenSent(t *testing.T) {
	a := newLeakAudit(t)
	a.importCode(t)
	a.start(t)
	a.metaEchoes(400)
	require.NoError(t, a.w.HandleRecipientJob(context.Background(), a.queue.Jobs[0]))
	a.markCompleted(t)
	a.assertNoLeak(t, "refused")

	a.queue.Jobs = nil
	status, data := a.retry(t)
	require.Equal(t, fasthttp.StatusOK, status, "%v", data)
	a.assertNoLeak(t, "retry-failed answered and queued")

	a.metaAnswers(200, `{"messages":[{"id":"wamid.leak"}]}`)
	require.NoError(t, a.w.HandleRecipientJob(context.Background(), a.queue.Jobs[0]))
	assert.Equal(t, models.MessageStatusSent, a.recipient(t).Status)
	a.assertNoLeak(t, "sent after the retry")
}

func TestOTPLeakAudit_UncertainThenReleasedThenSent(t *testing.T) {
	a := newLeakAudit(t)
	a.importCode(t)
	a.start(t)
	a.metaEchoes(502)
	require.NoError(t, a.w.HandleRecipientJob(context.Background(), a.queue.Jobs[0]))
	a.markCompleted(t)
	r := a.recipient(t)
	a.assertNoLeak(t, "uncertain")

	// Listing and the release (the CLI's core), with its audit entry.
	list, err := authmask.ListUncertain(a.app.DB, 2*time.Minute)
	require.NoError(t, err)
	raw, _ := json.Marshal(list)
	assert.NotContains(t, string(raw), a.code, "the uncertain list never shows a code")
	op := a.adminOperator(t)
	require.NoError(t, authmask.ReleaseUncertain(a.app.DB, authmask.ReleaseRequest{
		LocalReleaseEnabled: true, RecipientID: r.ID, OperatorEmail: op.Email, Reason: "Meta log shows the message was never accepted",
	}, templateutil.OTPPolicy{}, time.Now(), false))
	a.assertNoLeak(t, "released (audit entry written)")

	a.queue.Jobs = nil
	status, data := a.retry(t)
	require.Equal(t, fasthttp.StatusOK, status, "%v", data)
	a.metaAnswers(200, `{"messages":[{"id":"wamid.leak2"}]}`)
	require.NoError(t, a.w.HandleRecipientJob(context.Background(), a.queue.Jobs[0]))
	assert.Equal(t, models.MessageStatusSent, a.recipient(t).Status)
	a.assertNoLeak(t, "sent after the release")
}

func TestOTPLeakAudit_AnExpiredCodeIsDroppedAndNeverSent(t *testing.T) {
	a := newLeakAudit(t)
	a.importCode(t)
	a.start(t)
	// The retention window passes without a send: the sweep and the worker both see the later time.
	later := time.Now().Add(48 * time.Hour)
	a.w.Now = func() time.Time { return later }
	n, err := worker.ScrubExpiredSecrets(a.app.DB, later, templateutil.OTPPolicy{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(1))
	a.assertNoLeak(t, "retention sweep")
	require.NoError(t, a.w.HandleRecipientJob(context.Background(), a.queue.Jobs[0]))
	assert.Zero(t, a.calls.Load(), "nothing is sent")
	a.assertNoLeak(t, "job for an expired code")
}

// assertNoLeak looks everywhere. stage names the step, for the failure message.
func (a *leakAudit) assertNoLeak(t *testing.T, stage string) {
	t.Helper()
	if testing.Short() {
		t.Skip("the leak audit scans every table, Redis and the APIs: skipped in -short mode")
	}
	assert.Empty(t, a.findLeaks(t), "the code leaked at stage %q", stage)
}

// The audit is only worth something if it can see a leak: plant the code in each kind of
// place and check that the detector reports it.
func TestOTPLeakAudit_TheDetectorSeesAPlantedCode(t *testing.T) {
	if testing.Short() {
		t.Skip("scans every table: skipped in -short mode")
	}
	a := newLeakAudit(t)
	a.importCode(t) // the audit looks at a real, sealed recipient too
	contact := testutil.CreateTestContactWith(t, a.app.DB, a.org.ID, testutil.WithPhoneNumber("+5511999990000"))
	require.NoError(t, a.app.DB.Model(contact).Update("last_message_preview", "your code is "+a.code).Error)
	_, _ = a.logs.Write([]byte("level=info msg=oops " + a.code + "\n"))
	_, _ = a.pubsub.Write([]byte("chan " + a.code + "\n"))
	require.NoError(t, a.app.Redis.Set(context.Background(), "leak:planted:"+a.code[:4], "x"+a.code, time.Minute).Err())
	t.Cleanup(func() { a.app.Redis.Del(context.Background(), "leak:planted:"+a.code[:4]) })

	hits := strings.Join(a.findLeaks(t), " | ")

	assert.Contains(t, hits, "table contacts")
	assert.Contains(t, hits, "log output")
	assert.Contains(t, hits, "redis pub/sub")
	assert.Contains(t, hits, "redis key")
	assert.Contains(t, hits, "API GET /contacts")
}
