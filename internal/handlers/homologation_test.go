package handlers_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/antimalware"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Homologation of the antimalware policy against a REAL clamd on Linux (see docker/homologation and
// scripts/homologation/antimalware.sh). The phases run as separate `go test` processes over the same
// database and storage directory, with the script stopping, restarting and updating the engine in
// between:
//
//	A  engine up: clean, EICAR, quarantine, organizations, old files in batches, concurrency
//	B  engine down and engine hung (timeout): everything stays closed
//	C  engine back after a restart: what failed is reprocessed and released
//	D  signature update: clean files scanned with older signatures are re-checked
//
// They skip unless TEST_CLAMD_ADDR is set, so the normal suite never needs an engine.

func homologEngine(t *testing.T, addrEnv string, timeout time.Duration) *antimalware.Clamd {
	t.Helper()
	if os.Getenv("TEST_CLAMD_ADDR") == "" {
		t.Skip("TEST_CLAMD_ADDR not set: homologation runs only against a real clamd")
	}
	addr := os.Getenv(addrEnv)
	if addr == "" {
		t.Skipf("%s not set", addrEnv)
	}
	return &antimalware.Clamd{Addr: addr, Timeout: timeout}
}

func homologStorage(t *testing.T, app *handlers.App) string {
	t.Helper()
	dir := os.Getenv("HOMOLOG_STORAGE")
	if dir == "" {
		dir = "/state/uploads"
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))
	app.Config.Storage.LocalPath = dir
	return dir
}

func eicarText() []byte { return []byte(eicarTest) } // exactly the 68-byte standard test string

// mediaFiles writes n distinct files plus their messages (no verdict rows: as if they predated the policy).
func mediaFiles(t *testing.T, app *handlers.App, root string, org, contact uuid.UUID, prefix string, n int) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		rel := filepath.Join("images", fmt.Sprintf("%s-%03d.jpg", prefix, i))
		require.NoError(t, os.MkdirAll(filepath.Join(root, "images"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte(fmt.Sprintf("\xFF\xD8\xFF\xE0%s-%d-%d", prefix, i, time.Now().UnixNano())), 0o644))
		m := &models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, ContactID: contact,
			Direction: models.DirectionIncoming, MessageType: models.MessageTypeImage, MediaURL: rel, Status: models.MessageStatusDelivered}
		require.NoError(t, app.DB.Create(m).Error)
		ids = append(ids, m.ID)
	}
	return ids
}

func countScanStatus(t *testing.T, app *handlers.App, org uuid.UUID, source, status string) int64 {
	t.Helper()
	var n int64
	app.DB.Model(&models.FileScan{}).Where("organization_id = ? AND source = ? AND status = ?", org, source, status).Count(&n)
	return n
}

// ---- A: engine up --------------------------------------------------------------------------------

func TestHomolog_A(t *testing.T) {
	eng := homologEngine(t, "TEST_CLAMD_ADDR", 30*time.Second)
	f := newPhaseA(t)
	f.app.Scanner = eng
	root := homologStorage(t, f.app)
	v, err := eng.Version(context.Background())
	require.NoError(t, err)
	t.Logf("engine: %s", v)

	t.Run("1 clean file is released", func(t *testing.T) {
		code, att, raw := f.upload(t, f.owner, f.ch, "foto-limpa.png", tinyPNG(t))
		require.Equal(t, fasthttp.StatusOK, code, raw)
		assert.Equal(t, models.ScanClean, att.ScanStatus)
		assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(f.download(t, f.owner, att.ID, "")))
	})

	var infected uuid.UUID
	t.Run("2+3 EICAR is detected, quarantined, blocked and audited", func(t *testing.T) {
		code, att, raw := f.upload(t, f.owner, f.ch, "eicar.txt", eicarText())
		require.Equal(t, fasthttp.StatusOK, code, raw)
		assert.Equal(t, models.ScanInfected, att.ScanStatus)
		infected = att.ID
		row := scanRow(t, f.app, models.ScanSourceChatAttachment, att.ID)
		assert.Contains(t, row.Signature, "Eicar", "the real engine's signature name is recorded")
		require.NotNil(t, row.QuarantinedAt)
		var stored models.ChatAttachment
		require.NoError(t, f.app.DB.First(&stored, "id = ?", att.ID).Error)
		_, err := os.Stat(filepath.Join(root, stored.StoragePath))
		assert.True(t, os.IsNotExist(err), "no longer on a served path")
		_, err = os.Stat(filepath.Join(root, row.StoragePath))
		assert.NoError(t, err, "kept in quarantine")
		assert.Equal(t, fasthttp.StatusLocked, testutil.GetResponseStatusCode(f.download(t, f.owner, att.ID, "")))
		assert.Equal(t, fasthttp.StatusLocked, testutil.GetResponseStatusCode(f.download(t, f.owner, att.ID, "bytes=0-9")))
		var n int64
		f.app.DB.Model(&models.AuditLog{}).Where("resource_type = ? AND resource_id = ?", "file_scan", row.ID).Count(&n)
		assert.EqualValues(t, 1, n)
	})

	t.Run("11 another organization and a user without access see nothing", func(t *testing.T) {
		code, att, raw := f.upload(t, f.owner, f.ch, "so-da-org.png", append(tinyPNG(t), 'x'))
		require.Equal(t, fasthttp.StatusOK, code, raw)
		for _, u := range []*models.User{f.foreign, f.noPerm, f.spare} {
			code := testutil.GetResponseStatusCode(f.download(t, u, att.ID, ""))
			assert.Contains(t, []int{fasthttp.StatusForbidden, fasthttp.StatusNotFound}, code, "%s", u.FullName)
		}
		_ = infected
	})

	contact := testutil.CreateTestContact(t, f.app.DB, f.org.ID)
	t.Run("7 old files without a verdict are scanned gradually", func(t *testing.T) {
		const n = 30
		ids := mediaFiles(t, f.app, root, f.org.ID, contact.ID, "old", n)
		f.app.Config.Antimalware.BatchSize = 5
		began := time.Now()
		passes := 0
		for ; passes < 40; passes++ {
			f.app.FileScanTick(context.Background())
			if countScanStatus(t, f.app, f.org.ID, models.ScanSourceMessageMedia, models.ScanClean) >= n {
				passes++
				break
			}
		}
		took := time.Since(began)
		assert.Equal(t, int64(n), countScanStatus(t, f.app, f.org.ID, models.ScanSourceMessageMedia, models.ScanClean))
		assert.GreaterOrEqual(t, passes, n/5, "at most batch_size files per pass: the scan is gradual")
		t.Logf("%d legacy files cleared in %d passes, %s (%.1f files/s)", len(ids), passes, took.Round(time.Millisecond), float64(n)/took.Seconds())
	})

	t.Run("12 concurrent workers share the queue", func(t *testing.T) {
		const n = 60
		mediaFiles(t, f.app, root, f.org.ID, contact.ID, "conc", n)
		before := countScanStatus(t, f.app, f.org.ID, models.ScanSourceMessageMedia, models.ScanClean)
		workers := make([]*handlers.App, 3)
		for i := range workers {
			workers[i] = &handlers.App{Config: f.app.Config, DB: f.app.DB, Log: f.app.Log, Redis: f.app.Redis, Scanner: eng}
		}
		began := time.Now()
		var wg sync.WaitGroup
		for _, w := range workers {
			wg.Add(1)
			go func(w *handlers.App) {
				defer wg.Done()
				for i := 0; i < 25; i++ {
					w.FileScanTick(context.Background())
				}
			}(w)
		}
		wg.Wait()
		assert.Equal(t, before+n, countScanStatus(t, f.app, f.org.ID, models.ScanSourceMessageMedia, models.ScanClean), "every file cleared")
		var retried int64
		f.app.DB.Model(&models.FileScan{}).Where("organization_id = ? AND source = ? AND attempts > 0", f.org.ID, models.ScanSourceMessageMedia).Count(&retried)
		assert.Zero(t, retried, "no file failed or was taken twice into an error")
		t.Logf("%d files, 3 workers, %s", n, time.Since(began).Round(time.Millisecond))
	})
}

// ---- B: engine down, engine hung -------------------------------------------------------------------

func TestHomolog_B(t *testing.T) {
	f := newPhaseA(t)
	root := homologStorage(t, f.app)

	t.Run("4 engine unavailable keeps new and old files closed", func(t *testing.T) {
		f.app.Scanner = homologEngine(t, "HOMOLOG_DOWN_ADDR", 3*time.Second)
		code, att, raw := f.upload(t, f.owner, f.ch, "durante-queda.png", append(tinyPNG(t), []byte(fmt.Sprint(time.Now().UnixNano()))...))
		require.Equal(t, fasthttp.StatusOK, code, raw)
		assert.Equal(t, models.ScanError, att.ScanStatus)
		assert.Equal(t, fasthttp.StatusLocked, testutil.GetResponseStatusCode(f.download(t, f.owner, att.ID, "")))
		contact := testutil.CreateTestContact(t, f.app.DB, f.org.ID)
		mediaFiles(t, f.app, root, f.org.ID, contact.ID, "down", 6)
		tick(t, f.app, f.org.ID)
		assert.Zero(t, countScanStatus(t, f.app, f.org.ID, models.ScanSourceMessageMedia, models.ScanClean), "nothing is cleared without a verdict")
		assert.Positive(t, countScanStatus(t, f.app, f.org.ID, models.ScanSourceChatAttachment, models.ScanError))
	})

	t.Run("6 an engine that hangs times out and keeps the file closed", func(t *testing.T) {
		f.app.Scanner = homologEngine(t, "HOMOLOG_TARPIT_ADDR", 2*time.Second)
		began := time.Now()
		code, att, raw := f.upload(t, f.owner, f.ch, "durante-travamento.png", append(tinyPNG(t), []byte(fmt.Sprint(time.Now().UnixNano()+1))...))
		require.Equal(t, fasthttp.StatusOK, code, raw)
		assert.Less(t, time.Since(began), 8*time.Second, "gives up at its timeout")
		assert.Equal(t, models.ScanError, att.ScanStatus)
		row := scanRow(t, f.app, models.ScanSourceChatAttachment, att.ID)
		assert.NotEmpty(t, row.LastError)
		assert.Equal(t, fasthttp.StatusLocked, testutil.GetResponseStatusCode(f.download(t, f.owner, att.ID, "")))
	})
}

// ---- C: engine back after a restart -----------------------------------------------------------------

func TestHomolog_C(t *testing.T) {
	eng := homologEngine(t, "TEST_CLAMD_ADDR", 30*time.Second)
	app := newTestApp(t) // same database (kept), same storage
	app.Scanner = eng
	homologStorage(t, app)

	var waiting int64
	app.DB.Model(&models.FileScan{}).Where("status IN ?", []string{models.ScanError, models.ScanPending}).Count(&waiting)
	require.Positive(t, waiting, "phase B left files waiting for the engine")
	var infectedBefore int64
	app.DB.Model(&models.FileScan{}).Where("status = ?", models.ScanInfected).Count(&infectedBefore)

	// the backoff of phase B is minutes long: the administrator's retry (or the passage of time) brings them due
	require.NoError(t, app.DB.Model(&models.FileScan{}).Where("status = ?", models.ScanError).Update("next_attempt_at", time.Now().Add(-time.Second)).Error)
	app.Config.Antimalware.BatchSize = 20
	for i := 0; i < 30; i++ {
		app.FileScanTick(context.Background())
		var left int64
		app.DB.Model(&models.FileScan{}).Where("status IN ?", []string{models.ScanError, models.ScanPending}).Count(&left)
		if left == 0 {
			break
		}
	}
	var left int64
	app.DB.Model(&models.FileScan{}).Where("status IN ?", []string{models.ScanError, models.ScanPending}).Count(&left)
	assert.Zero(t, left, "everything that waited was reprocessed after the restart")
	var infectedAfter int64
	app.DB.Model(&models.FileScan{}).Where("status = ?", models.ScanInfected).Count(&infectedAfter)
	assert.Equal(t, infectedBefore, infectedAfter, "a restart rescues nothing that was infected")

	// the chat attachments of phase B can now be downloaded by their uploader
	var rows []models.ChatAttachment
	app.DB.Where("name LIKE ?", "durante-%").Find(&rows)
	require.NotEmpty(t, rows)
	f := &phaseA{app: app}
	for _, a := range rows {
		var u models.User
		require.NoError(t, app.DB.First(&u, "id = ?", a.UploaderID).Error)
		assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(f.download(t, &u, a.ID, "")), a.Name)
	}
}

// ---- D: signature update ----------------------------------------------------------------------------

func TestHomolog_D(t *testing.T) {
	eng := homologEngine(t, "TEST_CLAMD_ADDR", 30*time.Second)
	app := newTestApp(t)
	app.Scanner = eng
	homologStorage(t, app)
	current, err := eng.Version(context.Background())
	require.NoError(t, err)
	t.Logf("signatures now: %s", current)

	// files scanned long ago with older signatures (the script ran freshclam in between)
	require.NoError(t, app.DB.Model(&models.FileScan{}).Where("status = ?", models.ScanClean).
		Updates(map[string]any{"scanner_version": "ClamAV 0.0/1/Mon Jan  1 00:00:00 2024", "scanned_at": time.Now().Add(-30 * 24 * time.Hour)}).Error)
	var clean int64
	app.DB.Model(&models.FileScan{}).Where("status = ?", models.ScanClean).Count(&clean)
	require.Positive(t, clean)
	app.Config.Antimalware.RescanDays = 1
	app.Config.Antimalware.BatchSize = 20
	for i := 0; i < 40; i++ {
		app.FileScanTick(context.Background())
	}
	var stale int64
	app.DB.Model(&models.FileScan{}).Where("status = ? AND scanner_version <> ?", models.ScanClean, current).Count(&stale)
	assert.Zero(t, stale, "every clean file was re-checked with the current signatures")
	var still int64
	app.DB.Model(&models.FileScan{}).Where("status = ?", models.ScanClean).Count(&still)
	assert.Equal(t, clean, still, "and stays clean")
}
