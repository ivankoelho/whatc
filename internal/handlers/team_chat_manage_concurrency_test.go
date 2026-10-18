package handlers_test

import (
	"encoding/json"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	ws "github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// Concurrency and atomicity of the channel write path (criteria 14, 17, 19). They run
// against the real PostgreSQL of the test environment: the guarantees under test are
// row locks and transactions, which a mock cannot reproduce.

// callSafe is chatDo for goroutines: it never calls t.FailNow, and returns the status.
func callSafe(h func(*fastglue.Request) error, orgID, userID uuid.UUID, body any, path map[string]string) int {
	var req *fastglue.Request
	ctx := &fasthttp.RequestCtx{}
	if body != nil {
		ctx.Request.Header.SetContentType("application/json")
		ctx.Request.Header.SetMethod("POST")
		req = &fastglue.Request{RequestCtx: ctx}
		b, _ := json.Marshal(body)
		ctx.Request.SetBody(b)
	} else {
		ctx.Request.Header.SetMethod("GET")
		req = &fastglue.Request{RequestCtx: ctx}
	}
	testutil.SetAuthContext(req, orgID, userID)
	for k, v := range path {
		testutil.SetPathParam(req, k, v)
	}
	if err := h(req); err != nil {
		return -1
	}
	return ctx.Response.StatusCode()
}

// runTogether starts every fn at the same instant and waits for all of them.
func runTogether(fns ...func()) {
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	for _, fn := range fns {
		ready.Add(1)
		done.Add(1)
		go func(f func()) {
			defer done.Done()
			ready.Done()
			<-start
			f()
		}(fn)
	}
	ready.Wait()
	close(start)
	done.Wait()
}

func ownerCount(t *testing.T, f *phaseA, ch *models.ChatChannel) int64 {
	t.Helper()
	var n int64
	f.app.DB.Model(&models.ChatChannelMember{}).Where("channel_id = ? AND role = ?", ch.ID, "owner").Count(&n)
	return n
}

// Criterion 14(a): two owners leaving (or one leaving while the other demotes
// themselves) at the same moment never leave the channel without an owner.
func TestPhaseA_Concurrency_LastOwnerNeverLost(t *testing.T) {
	f := newPhaseA(t)
	second := chatUser(t, f.app, f.org.ID, teamChatKeysOf("agent"))

	for round := 0; round < 25; round++ {
		ch := f.freeChannel(t, "pair", f.owner)
		require.NoError(t, f.app.DB.Create(&models.ChatChannelMember{
			OrganizationID: f.org.ID, ChannelID: ch.ID, UserID: second.ID, Role: "owner"}).Error)
		path := f.path(ch)
		pathSelfA := f.path(ch, f.owner.ID.String())
		pathSelfB := f.path(ch, second.ID.String())

		var codeA, codeB int
		if round%2 == 0 {
			runTogether(
				func() { codeA = callSafe(f.app.LeaveChatChannel, f.org.ID, f.owner.ID, nil, path) },
				func() { codeB = callSafe(f.app.LeaveChatChannel, f.org.ID, second.ID, nil, path) },
			)
		} else {
			// One leaves while the other demotes themselves.
			runTogether(
				func() { codeA = callSafe(f.app.LeaveChatChannel, f.org.ID, f.owner.ID, nil, path) },
				func() {
					codeB = callSafe(f.app.SetChatMemberRole, f.org.ID, second.ID, map[string]any{"role": "member"}, pathSelfB)
				},
			)
			_ = pathSelfA
		}

		ok, conflict := 0, 0
		for _, c := range []int{codeA, codeB} {
			switch c {
			case fasthttp.StatusOK:
				ok++
			case fasthttp.StatusConflict:
				conflict++
			default:
				t.Fatalf("round %d: unexpected status %d (a deadlock would show as 500)", round, c)
			}
		}
		assert.Equal(t, 1, ok, "round %d: exactly one of the two wins", round)
		assert.Equal(t, 1, conflict, "round %d: the other is refused", round)
		assert.EqualValues(t, 1, ownerCount(t, f, ch), "round %d: the channel keeps exactly one owner", round)
	}
}

// Criterion 14(b): a message sent while the channel is being archived either lands
// before the archive or is refused. None lands after it.
func TestPhaseA_Concurrency_ArchiveVersusSend(t *testing.T) {
	f := newPhaseA(t)

	for round := 0; round < 6; round++ {
		ch := f.freeChannel(t, "busy", f.owner, f.member)
		path := f.path(ch)
		const senders = 30
		var accepted, refused, other int64

		fns := []func(){func() {
			code := callSafe(f.app.ArchiveChatChannel, f.org.ID, f.owner.ID, nil, path)
			if code != fasthttp.StatusOK {
				atomic.AddInt64(&other, 1)
			}
		}}
		for i := 0; i < senders; i++ {
			fns = append(fns, func() {
				switch callSafe(f.app.SendChatMessage, f.org.ID, f.member.ID, map[string]any{"body": "race"}, path) {
				case fasthttp.StatusOK:
					atomic.AddInt64(&accepted, 1)
				case fasthttp.StatusConflict:
					atomic.AddInt64(&refused, 1)
				default:
					atomic.AddInt64(&other, 1)
				}
			})
		}
		runTogether(fns...)

		assert.Zero(t, other, "round %d: only 200 or 409 (a deadlock or error would be 500)", round)
		assert.EqualValues(t, senders, accepted+refused, "round %d", round)

		fresh := f.reload(t, ch)
		require.NotNil(t, fresh.ArchivedAt, "round %d: the channel got archived", round)

		var userMsgs []models.ChatMessage
		require.NoError(t, f.app.DB.Where("channel_id = ? AND kind = ?", ch.ID, models.ChatMessageKindUser).Find(&userMsgs).Error)
		assert.EqualValues(t, accepted, len(userMsgs), "round %d: every accepted message is stored and nothing else", round)
		for _, m := range userMsgs {
			assert.False(t, m.CreatedAt.After(*fresh.ArchivedAt),
				"round %d: a message landed after the archive (%s > %s)", round, m.CreatedAt, fresh.ArchivedAt)
		}
		// And once archived, every later attempt is refused.
		assert.Equal(t, fasthttp.StatusConflict,
			callSafe(f.app.SendChatMessage, f.org.ID, f.member.ID, map[string]any{"body": "late"}, path))
	}
}

// Criterion 19: every kind of write on the same channel at once. The lock hierarchy
// (the channel row first, SHARE or UPDATE chosen up front, never upgraded) means no
// deadlock: a deadlock surfaces as a 500, so none may occur.
func TestPhaseA_Concurrency_NoDeadlockUnderMixedWrites(t *testing.T) {
	f := newPhaseA(t)
	ch := f.freeChannel(t, "storm", f.owner, f.member)
	extras := make([]*models.User, 4)
	for i := range extras {
		extras[i] = chatUser(t, f.app, f.org.ID, teamChatKeysOf("agent"))
	}
	path := f.path(ch)
	_, seed := sendMsg(t, f.app, f.org.ID, f.member.ID, ch.ID, "seed")
	seedPath := map[string]string{"id": seed.ID.String()}

	var mu sync.Mutex
	statuses := map[int]int{}
	record := func(code int) { mu.Lock(); statuses[code]++; mu.Unlock() }

	ops := []func(r *rand.Rand){
		func(r *rand.Rand) {
			record(callSafe(f.app.SendChatMessage, f.org.ID, f.member.ID, map[string]any{"body": "m"}, path))
		},
		func(r *rand.Rand) {
			record(callSafe(f.app.SendChatMessage, f.org.ID, f.owner.ID, map[string]any{"body": "o"}, path))
		},
		func(r *rand.Rand) {
			record(callSafe(f.app.UpdateChatMessage, f.org.ID, f.member.ID, map[string]any{"body": "edit"}, seedPath))
		},
		func(r *rand.Rand) {
			record(callSafe(f.app.AddChatChannelMember, f.org.ID, f.manager.ID, map[string]any{"user_id": extras[r.Intn(len(extras))].ID}, path))
		},
		func(r *rand.Rand) {
			record(callSafe(f.app.RemoveChatChannelMember, f.org.ID, f.manager.ID, nil,
				f.path(ch, extras[r.Intn(len(extras))].ID.String())))
		},
		func(r *rand.Rand) {
			record(callSafe(f.app.RenameChatChannel, f.org.ID, f.manager.ID, map[string]any{"name": "n"}, path))
		},
		func(r *rand.Rand) { record(callSafe(f.app.ArchiveChatChannel, f.org.ID, f.manager.ID, nil, path)) },
		func(r *rand.Rand) { record(callSafe(f.app.ReopenChatChannel, f.org.ID, f.manager.ID, nil, path)) },
		func(r *rand.Rand) {
			record(callSafe(f.app.SetChatMemberRole, f.org.ID, f.manager.ID, map[string]any{"role": "owner"}, f.path(ch, f.member.ID.String())))
		},
		func(r *rand.Rand) {
			record(callSafe(f.app.SetChatMemberRole, f.org.ID, f.manager.ID, map[string]any{"role": "member"}, f.path(ch, f.member.ID.String())))
		},
		func(r *rand.Rand) {
			record(callSafe(f.app.JoinChatChannel, f.org.ID, f.admin.ID, map[string]any{"reason": "storm test reason"}, path))
		},
		func(r *rand.Rand) {
			record(callSafe(f.app.NotifyChatTyping, f.org.ID, f.member.ID, map[string]any{}, path))
		},
		func(r *rand.Rand) { record(callSafe(f.app.MarkChatChannelRead, f.org.ID, f.member.ID, nil, path)) },
		func(r *rand.Rand) { record(callSafe(f.app.ListChatMessages, f.org.ID, f.member.ID, nil, path)) },
	}

	deadline := time.Now().Add(2 * time.Second)
	workers := make([]func(), 12)
	for w := range workers {
		seed := int64(w + 1)
		workers[w] = func() {
			r := rand.New(rand.NewSource(seed))
			for time.Now().Before(deadline) {
				ops[r.Intn(len(ops))](r)
			}
		}
	}
	runTogether(workers...)

	total := 0
	for code, n := range statuses {
		total += n
		assert.Contains(t, []int{200, 204, 400, 403, 404, 409}, code,
			"status %d appeared %d times: a deadlock or a failed transaction answers 500", code, n)
	}
	assert.Greater(t, total, 100, "the storm actually ran")
	assert.NotZero(t, statuses[200], "and real work got done")

	// The channel is still coherent: it exists, every member row is unique, and
	// every system message has a matching audit entry (none was written alone).
	fresh := f.reload(t, ch)
	assert.Equal(t, ch.ID, fresh.ID)
	var dup int64
	f.app.DB.Raw(`SELECT COUNT(*) FROM (SELECT user_id FROM chat_channel_members WHERE channel_id = ? GROUP BY user_id HAVING COUNT(*) > 1) d`, ch.ID).Scan(&dup)
	assert.Zero(t, dup)
	sys := len(f.systemMessages(t, ch))
	assert.Equal(t, sys, len(f.audits(t, ch)), "every change left both its system message and its audit entry")
}

// Criterion 17: if writing the audit entry fails, the change is not left behind:
// no membership, no system message, no rename/archive, no event. Then the same
// request succeeds once the audit works again.
func TestPhaseA_AuditFailureRollsEverythingBack(t *testing.T) {
	type op struct {
		name   string
		setup  func(f *phaseA)
		run    func(f *phaseA) int
		change string
	}
	ops := []op{
		{name: "admin join", change: "admin_joined", run: func(f *phaseA) int {
			return callSafe(f.app.JoinChatChannel, f.org.ID, f.admin.ID, map[string]any{"reason": "audit failure test"}, f.path(f.ch))
		}},
		{name: "add member", change: "member_added", run: func(f *phaseA) int {
			return callSafe(f.app.AddChatChannelMember, f.org.ID, f.owner.ID, map[string]any{"user_id": f.spare.ID}, f.path(f.ch))
		}},
		{name: "remove member", change: "member_removed", run: func(f *phaseA) int {
			return callSafe(f.app.RemoveChatChannelMember, f.org.ID, f.owner.ID, nil, f.path(f.ch, f.member.ID.String()))
		}},
		{name: "leave", change: "member_left", run: func(f *phaseA) int {
			return callSafe(f.app.LeaveChatChannel, f.org.ID, f.member.ID, nil, f.path(f.ch))
		}},
		{name: "change role", change: "owner_changed", run: func(f *phaseA) int {
			return callSafe(f.app.SetChatMemberRole, f.org.ID, f.owner.ID, map[string]any{"role": "owner"}, f.path(f.ch, f.member.ID.String()))
		}},
		{name: "rename", change: "renamed", run: func(f *phaseA) int {
			return callSafe(f.app.RenameChatChannel, f.org.ID, f.owner.ID, map[string]any{"name": "renamed"}, f.path(f.ch))
		}},
		{name: "archive", change: "archived", run: func(f *phaseA) int {
			return callSafe(f.app.ArchiveChatChannel, f.org.ID, f.owner.ID, nil, f.path(f.ch))
		}},
		{name: "reopen", change: "reopened",
			setup: func(f *phaseA) {
				require.NoError(t, f.app.DB.Model(f.ch).Update("archived_at", gorm.Expr("now()")).Error)
			},
			run: func(f *phaseA) int {
				return callSafe(f.app.ReopenChatChannel, f.org.ID, f.manager.ID, nil, f.path(f.ch))
			}},
	}

	for _, o := range ops {
		t.Run(o.name, func(t *testing.T) {
			f := newPhaseA(t)
			if o.setup != nil {
				o.setup(f)
			}
			cs := chatHub(t, f.app, f.org.ID, f.owner, f.member, f.spare, f.admin)

			type state struct {
				members map[uuid.UUID]string
				name    string
				archive bool
				sys     int
				audits  int
				msgs    int
			}
			snap := func() state {
				s := state{members: map[uuid.UUID]string{}}
				var rows []models.ChatChannelMember
				f.app.DB.Where("channel_id = ?", f.ch.ID).Find(&rows)
				for _, m := range rows {
					s.members[m.UserID] = m.Role
				}
				fresh := f.reload(t, f.ch)
				s.name, s.archive = fresh.Name, fresh.ArchivedAt != nil
				s.sys, s.audits = len(f.systemMessages(t, f.ch)), len(f.audits(t, f.ch))
				var n int64
				f.app.DB.Model(&models.ChatMessage{}).Where("channel_id = ?", f.ch.ID).Count(&n)
				s.msgs = int(n)
				return s
			}
			before := snap()

			restore := handlers.SetChatAuditWriterForTest(func(*gorm.DB, uuid.UUID, uuid.UUID, string, string, uuid.UUID, models.AuditAction, ...map[string]any) error {
				return errors.New("audit store is down")
			})
			code := o.run(f)
			restore()

			assert.Equal(t, fasthttp.StatusInternalServerError, code, "the failed audit fails the request")
			assert.Equal(t, before, snap(), "no membership, no system message, no rename/archive, no audit: nothing was left behind")
			for _, c := range cs {
				assertNoBroadcast(t, c) // and the event only goes out after a commit that never happened
			}

			// The same request works once the audit does, and then the event goes out.
			assert.Equal(t, fasthttp.StatusOK, o.run(f))
			after := snap()
			assert.Equal(t, before.sys+1, after.sys)
			assert.Equal(t, before.audits+1, after.audits)
			got := false
			for _, c := range cs {
				select {
				case m := <-c.SendChan():
					if m != nil {
						got = true
					}
				case <-time.After(300 * time.Millisecond):
				}
			}
			assert.True(t, got, "the event is sent after the commit")
			_ = ws.TypeTeamChatChannelUpdated
		})
	}
}
