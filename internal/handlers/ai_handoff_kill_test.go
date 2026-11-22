package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// P1-01 with a REAL process kill.
//
// The test binary re-runs itself as a helper process (TestAIHandoffKillHelper). The helper handles one
// customer message with the simulated WhatsApp server of the parent; at a named point of the handoff it
// writes a marker file and blocks. The parent sees the marker and kills the helper with Process.Kill():
// no deferred function, no recover, no cleanup runs, exactly like a crashed or OOM-killed server. The
// parent (a different App instance: "the restarted application") then runs recovery and checks the
// database and what the customer received.
//
// The helper only talks to the parent's fake WhatsApp server and to the test database; it needs the same
// PostgreSQL/Redis as the parent (TEST_DATABASE_URL / TEST_REDIS_URL), exclusive to the test run.

const (
	killEnvHelper = "AIKILL_HELPER"
	killEnvWAURL  = "AIKILL_WA_URL"
	killEnvPoint  = "AIKILL_POINT"
	killEnvSignal = "AIKILL_SIGNAL"
	killEnvPhone  = "AIKILL_PHONE_ID"
	killEnvFrom   = "AIKILL_FROM"
	killEncKey    = "test-encryption-key-for-handlers-longer-than-32-chars"
)

// TestAIHandoffKillHelper is the child process. It does nothing unless the parent started it.
func TestAIHandoffKillHelper(t *testing.T) {
	if os.Getenv(killEnvHelper) != "1" {
		t.Skip("helper process of the kill test")
	}
	log := testutil.NopLogger()
	db := testutil.SetupTestDB(t) // TEST_DB_KEEP=1 is set by the parent: the parent's fixtures must survive
	rdb := testutil.SetupTestRedis(t)
	require.NotNil(t, rdb)
	tr := &seqTransport{replies: []string{durableFarewell, qualHappy}}
	app := &App{
		Config:     &config.Config{App: config.AppConfig{EncryptionKey: killEncKey}},
		DB:         db,
		Log:        log,
		WhatsApp:   whatsapp.NewWithBaseURL(log, os.Getenv(killEnvWAURL)),
		HTTPClient: &http.Client{Transport: tr, Timeout: 5 * time.Second},
		Redis:      rdb,
	}
	point, signal := os.Getenv(killEnvPoint), os.Getenv(killEnvSignal)
	aiHandoffFault = func(p string) {
		if p == point {
			_ = os.WriteFile(signal, []byte(p), 0o600)
			select {} // wait for the parent's Process.Kill()
		}
	}
	var msg IncomingTextMessage
	raw := `{"from":"` + os.Getenv(killEnvFrom) + `","id":"wamid.` + uuid.NewString()[:8] + `","type":"text","text":{"body":` + mustJSON(durableCustomer) + `}}`
	require.NoError(t, json.Unmarshal([]byte(raw), &msg))
	app.processIncomingMessageFull(os.Getenv(killEnvPhone), msg, "Cliente")
	// reaching here means the kill point was never hit: the parent will fail on the missing marker
}

// killWorld is a wiring whose WhatsApp API is a server this test owns, so the child can reach it.
type killWorld struct {
	*wiring
	waURL string
	mu    sync.Mutex
	sends []string
}

func newKillWorld(t *testing.T) *killWorld {
	t.Helper()
	w := &killWorld{wiring: newDurableWiring(t)}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), "typing_indicator") {
			w.mu.Lock()
			w.sends = append(w.sends, string(raw))
			w.mu.Unlock()
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"messages": []map[string]string{{"id": "wamid.k" + uuid.NewString()[:8]}}})
	}))
	t.Cleanup(srv.Close)
	w.waURL = srv.URL
	w.app.WhatsApp = whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL) // the "restarted" app talks to the same fake
	return w
}

func (w *killWorld) sentCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.sends)
}

func (w *killWorld) waitMessages() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return countContaining(w.sends, "Vou chamar um atendente")
}

// killAt starts the child, waits until it reaches the point, and kills it abruptly.
func (w *killWorld) killAt(t *testing.T, point, from string) {
	t.Helper()
	signal := filepath.Join(t.TempDir(), "reached")
	cmd := exec.Command(os.Args[0], "-test.run=^TestAIHandoffKillHelper$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(),
		killEnvHelper+"=1", "TEST_DB_KEEP=1",
		killEnvWAURL+"="+w.waURL, killEnvPoint+"="+point, killEnvSignal+"="+signal,
		killEnvPhone+"="+w.f.account.PhoneID, killEnvFrom+"="+from)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(t, cmd.Start())

	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err := os.Stat(signal); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("the helper never reached %q. Output:\n%s", point, out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NoError(t, cmd.Process.Kill(), "abrupt kill: nothing in the helper gets to clean up")
	_ = cmd.Wait()
}

func TestHandoffRealKill_RecoveryAfterTheProcessDiesAtEveryPoint(t *testing.T) {
	cases := []struct {
		point          string
		farewellSent   bool // did the customer read the farewell before the kill
		transferBefore bool // did the attendance already exist when the process died
	}{
		{"before_farewell", false, false},
		{"after_farewell", true, false}, // the P1-01 case
		{"after_qualification", true, false},
		{"after_transfer", true, true},
	}
	for i, tc := range cases {
		t.Run(tc.point, func(t *testing.T) {
			w := newKillWorld(t)
			from := "5511900003" + string(rune('0'+i)) + "00"
			w.killAt(t, tc.point, from)

			c := w.contactFor(t, from)
			// state left by the dead process
			if tc.farewellSent {
				require.GreaterOrEqual(t, w.sentCount(), 1, "the customer read the farewell")
			} else {
				require.Zero(t, w.sentCount(), "nothing had been said yet")
			}
			require.Equal(t, tc.transferBefore, len(w.aiTransfers(t, c.ID)) == 1)
			in := w.intents(t, c.ID)
			require.Len(t, in, 1)
			require.Equal(t, models.AIHandoffIntentPending, in[0].Status, "the promise survived the kill")
			sentAtDeath := w.sentCount()

			// the application restarts: another App instance runs recovery (the lease of the dead run has passed)
			w.ai.replies = []string{qualHappy} // only the qualification call can still happen
			require.Equal(t, 1, w.app.recoverAIHandoffs(afterLease()))

			assert.Len(t, w.aiTransfers(t, c.ID), 1, "exactly one attendance, never two")
			assert.Len(t, w.opportunities(t, c.ID), map[bool]int{true: 1, false: 1}[tc.farewellSent], "one opportunity")
			assert.Equal(t, models.AIHandoffIntentDone, w.intents(t, c.ID)[0].Status)
			assert.LessOrEqual(t, w.waitMessages(), 1, "at most one waiting message")
			if tc.transferBefore {
				assert.Equal(t, sentAtDeath, w.sentCount(), "the attendance already existed: nothing more is said")
			}
			// idempotent: another recovery pass changes nothing
			assert.Zero(t, w.app.recoverAIHandoffs(afterLease()))
			assert.Len(t, w.aiTransfers(t, c.ID), 1)
		})
	}
}

// A person who took the conversation after the process died is never overridden by the recovery.
func TestHandoffRealKill_AHumanWhoTookOverIsNotOverwritten(t *testing.T) {
	w := newKillWorld(t)
	const from = "5511900004000"
	w.killAt(t, "after_farewell", from)
	c := w.contactFor(t, from)
	w.openAttendance(t, c.ID) // an attendant opens the conversation while the application is down
	sentAtDeath := w.sentCount()

	require.Equal(t, 1, w.app.recoverAIHandoffs(afterLease()))

	assert.Empty(t, w.aiTransfers(t, c.ID), "the agent never competes with a person")
	assert.Equal(t, sentAtDeath, w.sentCount(), "nothing is said over the attendant")
	var active int64
	w.app.DB.Model(&models.AgentTransfer{}).Where("contact_id = ? AND status = ?", c.ID, models.TransferStatusActive).Count(&active)
	assert.EqualValues(t, 1, active, "the human's attendance is the only one")
	assert.Equal(t, aiHandoffHumanActive, w.intents(t, c.ID)[0].Outcome)
}
