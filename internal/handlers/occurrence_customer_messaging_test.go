package handlers_test

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// R2: writing to a customer from a case takes chat:write AND being someone who may
// use the case's channel, on every endpoint, and every protocol send leaves a trace.
// The mock Meta server is the only thing that could ever receive a message.

const protocolAuditType = "occurrence_message"

type msgWorld struct {
	app      *handlers.App
	m        *mockWhatsAppServer
	org      *models.Organization
	agent    *models.User // assignee, authorized (chat:write)
	jur      *models.User // Juridico-like: occurrences:write, NO chat:write, owns occJur
	chatOnly *models.User // chat:write, no occurrences:write
	sup      *models.User // sees every case (occurrences:view_all) with chat, but no conversation scope
	outsider *models.User // another org, every permission
	occ      *models.Occurrence
	occJur   *models.Occurrence
	contact  *models.Contact
	account  *models.WhatsAppAccount
}

func newMsgWorld(t *testing.T) *msgWorld {
	t.Helper()
	app, m, org, agent, occ, contact := sendFixture(t, true)
	w := &msgWorld{app: app, m: m, org: org, agent: agent, occ: occ, contact: contact}
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, contact.WhatsAppAccount).First(&w.account).Error)

	mk := func(name string, keys ...string) *models.User {
		role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, name+"-"+uuid.New().String()[:5], keys)
		return testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	}
	// chat:read on purpose: the only thing this user lacks is chat:write, so a refusal
	// can only come from that permission.
	w.jur = mk("jur", "occurrences:read", "occurrences:write", "contacts:read", "chat:read")
	w.chatOnly = mk("chatonly", "chat:read", "chat:write")
	w.sup = mk("sup", "occurrences:read", "occurrences:write", "occurrences:view_all", "chat:read", "chat:write")

	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	otherRole := testutil.CreateTestRoleWithKeys(t, app.DB, otherOrg.ID, "other-all",
		[]string{"occurrences:read", "occurrences:write", "occurrences:view_all", "chat:read", "chat:write", "conversations:view_all"})
	w.outsider = testutil.CreateTestUser(t, app.DB, otherOrg.ID, testutil.WithRoleID(&otherRole.ID))

	// A case the Juridico owns, on the same contact (so the window is open).
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	jurOcc := models.Occurrence{OrganizationID: org.ID, ContactID: &contact.ID, Title: "Caso do juridico",
		StageID: stage.ID, OpenedByUserID: w.jur.ID, AssignedUserID: &w.jur.ID}
	require.NoError(t, app.CreateOccurrenceForTest(&jurOcc))
	w.occJur = &jurOcc
	return w
}

func (w *msgWorld) closeWindow(t *testing.T) {
	t.Helper()
	require.NoError(t, w.app.DB.Model(w.contact).Update("last_inbound_at", time.Now().Add(-30*time.Hour)).Error)
}

func (w *msgWorld) protocol(t *testing.T, user *models.User, occ *models.Occurrence, body []byte) (int, []byte) {
	t.Helper()
	req := sendProtocol(t, w.app, user.OrganizationID, user.ID, occ, body)
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
}

func (w *msgWorld) reply(t *testing.T, user *models.User, occ *models.Occurrence, content string) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]string{"content": content})
	testutil.SetAuthContext(req, user.OrganizationID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, w.app.ReplyToOccurrence(req))
	return testutil.GetResponseStatusCode(req)
}

// nothingLeft asserts the customer-facing side saw nothing and left no record.
func (w *msgWorld) nothingLeft(t *testing.T, label string) {
	t.Helper()
	time.Sleep(150 * time.Millisecond) // sends are async in places; give a wrong send time to show up
	assert.Empty(t, w.m.sentMessages, label+": the mock Meta server must receive nothing")
	var msgs, events, audits int64
	require.NoError(t, w.app.DB.Model(&models.Message{}).Where("contact_id = ? AND direction = ?", w.contact.ID, models.DirectionOutgoing).Count(&msgs).Error)
	require.NoError(t, w.app.DB.Model(&models.OccurrenceEvent{}).Where("organization_id = ? AND type = ?", w.org.ID, models.OccurrenceEventProtocolSent).Count(&events).Error)
	require.NoError(t, w.app.DB.Model(&models.AuditLog{}).Where("organization_id = ? AND resource_type = ?", w.org.ID, protocolAuditType).Count(&audits).Error)
	assert.Zero(t, msgs, label+": no outgoing message row")
	assert.Zero(t, events, label+": no protocol_sent event")
	assert.Zero(t, audits, label+": no send record")
}

// --- send-protocol ---------------------------------------------------------------

func TestSendProtocol_RefusedWithoutChatWrite_EvenOnOwnCase(t *testing.T) {
	w := newMsgWorld(t)

	for name, body := range map[string][]byte{
		"standard text": nil,
		"free text":     []byte(`{"message":"texto livre sem permissao de chat"}`),
	} {
		code, _ := w.protocol(t, w.jur, w.occJur, body)
		assert.Equal(t, fasthttp.StatusForbidden, code, name)
	}
	w.nothingLeft(t, "juridico")
}

func TestSendProtocol_RefusalDoesNotRevealWindowOrAccount(t *testing.T) {
	w := newMsgWorld(t)
	w.closeWindow(t)
	// A closed window answers 422 to someone allowed to send; to the Juridico it must
	// still be the plain 403, with nothing about the window or the account.
	code, body := w.protocol(t, w.jur, w.occJur, []byte(`{"message":"x"}`))
	assert.Equal(t, fasthttp.StatusForbidden, code)
	assert.NotContains(t, string(body), "window")
	assert.NotContains(t, string(body), w.account.Name)

	code, _ = w.protocol(t, w.agent, w.occ, nil)
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, code, "an authorized agent still learns the window is closed")
	w.nothingLeft(t, "closed window")
}

func TestSendProtocol_AuthorizationMatrix(t *testing.T) {
	cases := []struct {
		name string
		who  func(w *msgWorld) *models.User
		occ  func(w *msgWorld) *models.Occurrence
		want int
		sent bool
	}{
		{"assignee with chat:write", func(w *msgWorld) *models.User { return w.agent }, func(w *msgWorld) *models.Occurrence { return w.occ }, fasthttp.StatusOK, true},
		{"chat:write without occurrences:write", func(w *msgWorld) *models.User { return w.chatOnly }, func(w *msgWorld) *models.Occurrence { return w.occ }, fasthttp.StatusForbidden, false},
		{"sees every case + chat, no conversation scope", func(w *msgWorld) *models.User { return w.sup }, func(w *msgWorld) *models.Occurrence { return w.occ }, fasthttp.StatusForbidden, false},
		{"another organization, every permission", func(w *msgWorld) *models.User { return w.outsider }, func(w *msgWorld) *models.Occurrence { return w.occ }, fasthttp.StatusNotFound, false},
		{"agent on a case of someone else", func(w *msgWorld) *models.User { return w.agent }, func(w *msgWorld) *models.Occurrence { return w.occJur }, fasthttp.StatusForbidden, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newMsgWorld(t)
			code, _ := w.protocol(t, c.who(w), c.occ(w), nil)
			assert.Equal(t, c.want, code)
			if c.sent {
				require.Eventually(t, func() bool { return len(w.m.sentMessages) == 1 }, time.Second, 10*time.Millisecond)
			} else {
				w.nothingLeft(t, c.name)
			}
		})
	}
}

// Scope: having the permissions is not enough; the person must be on the case's
// channel (assignee, case or account team, or conversations:view_all).
func TestSendProtocol_ScopeOfTheChannel(t *testing.T) {
	w := newMsgWorld(t)
	code, _ := w.protocol(t, w.sup, w.occ, nil)
	require.Equal(t, fasthttp.StatusForbidden, code)
	w.nothingLeft(t, "sup before")

	// conversations:view_all is the explicit grant that opens every channel.
	role := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "sup-all-"+uuid.New().String()[:5],
		[]string{"occurrences:read", "occurrences:write", "occurrences:view_all", "chat:read", "chat:write", "conversations:view_all"})
	supAll := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&role.ID))
	code, _ = w.protocol(t, supAll, w.occ, nil)
	assert.Equal(t, fasthttp.StatusOK, code)

	// Membership of the account's team is the other grant (same rule the chat uses).
	team := &models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID, Name: "Equipe da conta", IsActive: true}
	require.NoError(t, w.app.DB.Create(team).Error)
	require.NoError(t, w.app.DB.Model(w.account).Update("default_team_id", team.ID).Error)
	require.NoError(t, w.app.DB.Create(&models.TeamMember{BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: w.sup.ID, Role: models.TeamRoleAgent}).Error)
	code, _ = w.protocol(t, w.sup, w.occ, nil)
	assert.Equal(t, fasthttp.StatusOK, code, "a member of the account's team may use its channel")
}

// --- /reply ----------------------------------------------------------------------

func TestReply_AuthorizationMatrix(t *testing.T) {
	w := newMsgWorld(t)

	assert.Equal(t, fasthttp.StatusForbidden, w.reply(t, w.jur, w.occJur, "sem chat"), "no chat:write")
	assert.Equal(t, fasthttp.StatusForbidden, w.reply(t, w.chatOnly, w.occ, "sem occurrences:write"))
	assert.Equal(t, fasthttp.StatusForbidden, w.reply(t, w.sup, w.occ, "ve todos os casos, sem escopo de conversa"),
		"chat:write alone must not answer any conversation of the organization")
	assert.Equal(t, fasthttp.StatusNotFound, w.reply(t, w.outsider, w.occ, "outra organizacao"))
	assert.Equal(t, fasthttp.StatusForbidden, w.reply(t, w.agent, w.occJur, "caso de outra pessoa"))
	w.nothingLeft(t, "reply refusals")

	assert.Equal(t, fasthttp.StatusOK, w.reply(t, w.agent, w.occ, "resposta autorizada"))
	require.Eventually(t, func() bool { return len(w.m.sentMessages) == 1 }, time.Second, 10*time.Millisecond)
}

func TestReply_RefusalDoesNotRevealTheWindow(t *testing.T) {
	w := newMsgWorld(t)
	w.closeWindow(t)
	assert.Equal(t, fasthttp.StatusForbidden, w.reply(t, w.sup, w.occ, "x"), "no scope: 403 before any 422")
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, w.reply(t, w.agent, w.occ, "x"))
}

// A reply is attributed to the agent in our records (the row says who), while the
// customer-facing text carries no signature of its own.
func TestReply_RecordsAuthorAndContent(t *testing.T) {
	w := newMsgWorld(t)
	require.Equal(t, fasthttp.StatusOK, w.reply(t, w.agent, w.occ, "conteudo exato da resposta"))
	var msg models.Message
	require.NoError(t, w.app.DB.Where("contact_id = ? AND direction = ?", w.contact.ID, models.DirectionOutgoing).Order("created_at DESC").First(&msg).Error)
	assert.Equal(t, "conteudo exato da resposta", msg.Content)
	require.NotNil(t, msg.SentByUserID)
	assert.Equal(t, w.agent.ID, *msg.SentByUserID)
	assert.Equal(t, w.account.Name, msg.WhatsAppAccount)
}

// --- every other endpoint that writes to a customer ----------------------------

// The chat endpoints did not check chat:write at all: anyone with access to the
// conversation (an assigned contact, a team queue) could write to the customer.
func TestChatEndpoints_RequireChatWrite(t *testing.T) {
	w := newMsgWorld(t)
	// Someone who owns the conversation but whose role has no chat:write.
	role := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "owner-nochat-"+uuid.New().String()[:5], []string{"contacts:read"})
	owner := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&role.ID))
	require.NoError(t, w.app.DB.Model(w.contact).Update("assigned_user_id", owner.ID).Error)

	call := func(h func(*fastglue.Request) error, body any, params map[string]string) int {
		req := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(req, w.org.ID, owner.ID)
		for k, v := range params {
			testutil.SetPathParam(req, k, v)
		}
		require.NoError(t, h(req))
		return testutil.GetResponseStatusCode(req)
	}
	id := map[string]string{"id": w.contact.ID.String()}
	endpoints := map[string]func() int{
		"SendMessage": func() int {
			return call(w.app.SendMessage, map[string]any{"type": "text", "content": map[string]string{"body": "oi"}}, id)
		},
		"SendMediaMessage": func() int {
			return call(w.app.SendMediaMessage, map[string]any{"contact_id": w.contact.ID.String(), "media_url": "https://example.invalid/x.png"}, nil)
		},
		"SendTemplateMessage": func() int {
			return call(w.app.SendTemplateMessage, map[string]any{"contact_id": w.contact.ID.String(), "template_name": "x"}, nil)
		},
		"SendReaction": func() int {
			return call(w.app.SendReaction, map[string]any{"emoji": "👍"},
				map[string]string{"id": w.contact.ID.String(), "message_id": uuid.New().String()})
		},
	}
	for name, run := range endpoints {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, fasthttp.StatusForbidden, run(), "owning the conversation is not the right to write to the customer")
		})
	}
	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, w.m.sentMessages, "the mock Meta server must receive nothing")

	// Control: the same person with chat:write gets past the gate (the request may
	// still fail on its own merits, but never with the permission 403).
	role2 := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "owner-chat-"+uuid.New().String()[:5], []string{"contacts:read", "chat:read", "chat:write"})
	owner2 := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&role2.ID))
	require.NoError(t, w.app.DB.Model(w.contact).Update("assigned_user_id", owner2.ID).Error)
	req := testutil.NewJSONRequest(t, map[string]any{"type": "text", "content": map[string]string{"body": "oi"}})
	testutil.SetAuthContext(req, w.org.ID, owner2.ID)
	testutil.SetPathParam(req, "id", w.contact.ID.String())
	require.NoError(t, w.app.SendMessage(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	require.Eventually(t, func() bool { return len(w.m.sentMessages) == 1 }, time.Second, 10*time.Millisecond)
}

// --- traceability ----------------------------------------------------------------

func auditRows(t *testing.T, db *gorm.DB, occ uuid.UUID) []models.AuditLog {
	t.Helper()
	var rows []models.AuditLog
	require.NoError(t, db.Where("resource_type = ? AND resource_id = ?", protocolAuditType, occ).Order("created_at ASC").Find(&rows).Error)
	return rows
}

func changeOf(t *testing.T, row models.AuditLog, field string) any {
	t.Helper()
	for _, c := range row.Changes {
		if m, ok := c.(map[string]any); ok && m["field"] == field {
			return m["new_value"]
		}
	}
	return nil
}

func TestSendProtocol_RecordsAuthorProtocolContentChannelAndResult(t *testing.T) {
	w := newMsgWorld(t)
	text := "Texto exato enviado ao cliente"
	code, _ := w.protocol(t, w.agent, w.occ, []byte(fmt.Sprintf(`{"message":%q}`, text)))
	require.Equal(t, fasthttp.StatusOK, code)

	rows := auditRows(t, w.app.DB, w.occ.ID)
	require.Len(t, rows, 2, "one record before the send and one with its result, append-only")
	attempt, result := rows[0], rows[1]
	for _, r := range rows {
		assert.Equal(t, w.agent.ID, r.UserID, "author")
		assert.Equal(t, w.agent.FullName, r.UserName)
		assert.Equal(t, w.org.ID, r.OrganizationID)
		assert.Equal(t, w.occ.ProtocolNumber, changeOf(t, r, "protocol_number"))
		assert.Equal(t, text, changeOf(t, r, "content"), "the content actually sent")
		assert.Equal(t, w.account.Name, changeOf(t, r, "channel"))
		assert.False(t, r.CreatedAt.IsZero(), "date and time")
	}
	assert.Equal(t, "attempted", changeOf(t, attempt, "result"))
	assert.Equal(t, "sent", changeOf(t, result, "result"))
	assert.NotEmpty(t, changeOf(t, result, "message_id"))

	// The customer-facing text is exactly the content: no employee signature added.
	require.Eventually(t, func() bool { return len(w.m.sentMessages) == 1 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, text, w.m.sentMessages[0]["text"].(map[string]any)["body"])
	assert.NotContains(t, text+fmt.Sprint(w.m.sentMessages[0]), w.agent.FullName)
	var msg models.Message
	require.NoError(t, w.app.DB.Where("contact_id = ? AND direction = ?", w.contact.ID, models.DirectionOutgoing).First(&msg).Error)
	assert.Nil(t, msg.SentByUserID, "sent in the company's name: the message itself carries no author")
}

func TestSendProtocol_StandardTextIsRecordedToo(t *testing.T) {
	w := newMsgWorld(t)
	code, _ := w.protocol(t, w.agent, w.occ, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	rows := auditRows(t, w.app.DB, w.occ.ID)
	require.Len(t, rows, 2)
	assert.Contains(t, fmt.Sprint(changeOf(t, rows[1], "content")), w.occ.ProtocolNumber)
}

func TestSendProtocol_ProviderFailureIsAnsweredAndRecorded(t *testing.T) {
	w := newMsgWorld(t)
	w.m.returnError = true
	w.m.errorMessage = "provider says no"

	code, _ := w.protocol(t, w.agent, w.occ, []byte(`{"message":"vai falhar"}`))
	assert.Equal(t, fasthttp.StatusBadGateway, code, "the user is told it did not go out")

	rows := auditRows(t, w.app.DB, w.occ.ID)
	require.Len(t, rows, 2)
	assert.Equal(t, "attempted", changeOf(t, rows[0], "result"))
	assert.Equal(t, "failed", changeOf(t, rows[1], "result"))
	assert.NotEmpty(t, changeOf(t, rows[1], "error"))

	var events int64
	require.NoError(t, w.app.DB.Model(&models.OccurrenceEvent{}).Where("organization_id = ? AND type = ?", w.org.ID, models.OccurrenceEventProtocolSent).Count(&events).Error)
	assert.Zero(t, events, "a protocol that did not go out must not appear as sent in the timeline")
	var msg models.Message
	require.NoError(t, w.app.DB.Where("contact_id = ? AND direction = ?", w.contact.ID, models.DirectionOutgoing).First(&msg).Error)
	assert.Equal(t, models.MessageStatusFailed, msg.Status)
}

// If the record before the send cannot be written, nothing is sent: no message may
// reach a customer without leaving a trace.
func TestSendProtocol_RecordWriteFailureStopsTheSend(t *testing.T) {
	w := newMsgWorld(t)
	const cb = "test:fail_audit_create"
	require.NoError(t, w.app.DB.Callback().Create().Before("gorm:create").Register(cb, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" {
			_ = tx.AddError(errors.New("simulated audit write failure"))
		}
	}))
	t.Cleanup(func() { _ = w.app.DB.Callback().Create().Remove(cb) })

	code, _ := w.protocol(t, w.agent, w.occ, []byte(`{"message":"nao pode sair"}`))
	assert.Equal(t, fasthttp.StatusInternalServerError, code)
	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, w.m.sentMessages, "nothing may reach the customer untraced")
	var msgs int64
	require.NoError(t, w.app.DB.Model(&models.Message{}).Where("contact_id = ? AND direction = ?", w.contact.ID, models.DirectionOutgoing).Count(&msgs).Error)
	assert.Zero(t, msgs)
}

// If only the record AFTER the send fails, the message is already out: the answer
// says so, and the first record still exists.
func TestSendProtocol_ResultWriteFailureDoesNotHideAMessageThatWentOut(t *testing.T) {
	w := newMsgWorld(t)
	var created atomic.Int32
	const cb = "test:fail_second_audit_create"
	require.NoError(t, w.app.DB.Callback().Create().Before("gorm:create").Register(cb, func(tx *gorm.DB) {
		if tx.Statement.Table == "audit_logs" && created.Add(1) >= 2 {
			_ = tx.AddError(errors.New("simulated audit write failure"))
		}
	}))
	t.Cleanup(func() { _ = w.app.DB.Callback().Create().Remove(cb) })

	code, _ := w.protocol(t, w.agent, w.occ, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
	require.Eventually(t, func() bool { return len(w.m.sentMessages) == 1 }, time.Second, 10*time.Millisecond)
	rows := auditRows(t, w.app.DB, w.occ.ID)
	require.Len(t, rows, 1, "the attempt record remains")
	assert.Equal(t, "attempted", changeOf(t, rows[0], "result"))
}

// --- concurrency -------------------------------------------------------------------

// Many simultaneous sends: each produces exactly one message and its two records;
// nothing is lost or doubled.
func TestSendProtocol_ConcurrentSendsOnOneCase_NeverOverlap(t *testing.T) {
	w := newMsgWorld(t)
	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := testutil.NewJSONRequest(t, nil)
			req.RequestCtx.Request.SetBody([]byte(fmt.Sprintf(`{"message":"envio %d"}`, i)))
			testutil.SetAuthContext(req, w.org.ID, w.agent.ID)
			testutil.SetPathParam(req, "id", w.occ.ID.String())
			_ = w.app.SendOccurrenceProtocol(withKey(req))
			codes[i] = testutil.GetResponseStatusCode(req)
		}(i)
	}
	wg.Wait()
	// One case, one send at a time: a request either got through (200) or found another send open (409).
	sent := 0
	for i, c := range codes {
		assert.Contains(t, []int{fasthttp.StatusOK, fasthttp.StatusConflict}, c, "request %d", i)
		if c == fasthttp.StatusOK {
			sent++
		}
	}
	require.GreaterOrEqual(t, sent, 1)
	var msgs int64
	require.NoError(t, w.app.DB.Model(&models.Message{}).Where("contact_id = ? AND direction = ?", w.contact.ID, models.DirectionOutgoing).Count(&msgs).Error)
	assert.EqualValues(t, sent, msgs, "exactly one message per accepted request, none for a refused one")
	rows := auditRows(t, w.app.DB, w.occ.ID)
	assert.Len(t, rows, 2*sent, "a refused request leaves no attempt record: nothing was started")
	var open int64
	require.NoError(t, w.app.DB.Model(&models.OccurrenceSendIntent{}).Where("occurrence_id = ? AND state IN ?", w.occ.ID, []string{"processing", "uncertain"}).Count(&open).Error)
	assert.LessOrEqual(t, open, int64(1))
}

// Under load the refusals stay refusals: no unauthorized request slips through.
func TestSendProtocol_ConcurrentUnauthorizedRequestsAllRefused(t *testing.T) {
	w := newMsgWorld(t)
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := testutil.NewJSONRequest(t, nil)
			req.RequestCtx.Request.SetBody([]byte(`{"message":"x"}`))
			testutil.SetAuthContext(req, w.org.ID, w.jur.ID)
			testutil.SetPathParam(req, "id", w.occJur.ID.String())
			_ = w.app.SendOccurrenceProtocol(withKey(req))
			if testutil.GetResponseStatusCode(req) == fasthttp.StatusOK {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Zero(t, ok.Load())
	w.nothingLeft(t, "concurrent refusals")
}

// --- what the interface is told ----------------------------------------------------

// The case's `can` flags are the single answer the interface follows for showing the
// send-protocol and reply actions, so they must say what the endpoints will accept.
func TestGetOccurrence_CanFlagsFollowTheSendRule(t *testing.T) {
	w := newMsgWorld(t)
	flags := func(user *models.User, occ *models.Occurrence) map[string]bool {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, user.OrganizationID, user.ID)
		testutil.SetPathParam(req, "id", occ.ID.String())
		require.NoError(t, w.app.GetOccurrence(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), user.FullName)
		var out struct {
			Data struct {
				Can map[string]bool `json:"can"`
			} `json:"data"`
		}
		testutil.ParseJSONResponse(t, req, &out)
		return out.Data.Can
	}

	agent := flags(w.agent, w.occ)
	assert.True(t, agent["send_protocol"])
	assert.True(t, agent["reply"])

	jur := flags(w.jur, w.occJur) // owns the case, but has no chat:write
	assert.False(t, jur["send_protocol"], "the button must not be offered without chat:write")
	assert.False(t, jur["reply"])

	sup := flags(w.sup, w.occ) // sees every case and has chat:write, but no tie to the conversation
	assert.False(t, sup["send_protocol"])
	assert.False(t, sup["reply"])

	// And the endpoints agree with what was shown.
	code, _ := w.protocol(t, w.jur, w.occJur, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	code, _ = w.protocol(t, w.sup, w.occ, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
}

// --- who can read what was sent -----------------------------------------------------

// The text actually sent to a customer sits in the audit trail, which only people
// with audit access can read.
func TestSendProtocol_RecordIsReadableOnlyThroughAuditAccess(t *testing.T) {
	w := newMsgWorld(t)
	text := "conteudo que so a auditoria le"
	code, _ := w.protocol(t, w.agent, w.occ, []byte(fmt.Sprintf(`{"message":%q}`, text)))
	require.Equal(t, fasthttp.StatusOK, code)

	list := func(user *models.User) (int, string) {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, user.OrganizationID, user.ID)
		testutil.SetQueryParam(req, "resource_type", protocolAuditType)
		testutil.SetQueryParam(req, "resource_id", w.occ.ID.String())
		require.NoError(t, w.app.ListAuditLogs(req))
		return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
	}

	status, body := list(w.agent) // can send, cannot audit
	assert.Equal(t, fasthttp.StatusForbidden, status)
	assert.NotContains(t, body, text)

	role := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "auditor-"+uuid.New().String()[:5], []string{"audit_logs:read"})
	auditor := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&role.ID))
	status, body = list(auditor)
	assert.Equal(t, fasthttp.StatusOK, status)
	assert.Contains(t, body, text)
	assert.Contains(t, body, w.agent.FullName, "who sent it")
}

// withKey gives the request a fresh Idempotency-Key, as the interface does for every new
// intention to send. The protocol endpoint refuses a request without one.
func withKey(req *fastglue.Request) *fastglue.Request {
	testutil.SetHeader(req, "Idempotency-Key", uuid.New().String())
	return req
}
