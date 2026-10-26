package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// The purge engine is prepared and OFF. These tests drive it through a FAKE store (a test-only constructor
// lets it execute); production code has no way to, and the real store is never wired.

type fakeStore struct {
	mu          sync.Mutex
	records     map[uuid.UUID]int // DeleteRecord calls per resource
	files       map[string]int    // DeleteFile calls per path
	order       []string          // "record:<id>" / "file:<path>" in call order
	failRecord  map[uuid.UUID]int // fail the first N record deletions of a resource
	failFile    map[string]int    // fail the first N file deletions of a path
	deletedOrgs map[uuid.UUID]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{records: map[uuid.UUID]int{}, files: map[string]int{}, failRecord: map[uuid.UUID]int{}, failFile: map[string]int{}, deletedOrgs: map[uuid.UUID]bool{}}
}

func (f *fakeStore) DeleteRecord(_ context.Context, _ string, org, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[id]++
	f.order = append(f.order, "record:"+id.String())
	f.deletedOrgs[org] = true
	if f.failRecord[id] > 0 {
		f.failRecord[id]--
		return errors.New("database unavailable")
	}
	return nil
}

func (f *fakeStore) DeleteFile(_ context.Context, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[p]++
	f.order = append(f.order, "file:"+p)
	if f.failFile[p] > 0 {
		f.failFile[p]--
		return errors.New("disk error")
	}
	return nil
}

func (f *fakeStore) touched() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records) + len(f.files)
}

// engineWorld: n old files of a closed protocol, a policy of 12 months.
func (w *retWorld) oldFiles(t *testing.T, n int) []uuid.UUID {
	t.Helper()
	var ids []uuid.UUID
	for i := 0; i < n; i++ {
		f := w.upl(t, w.logPorto, fmt.Sprintf("f%02d.pdf", i), append(pdfBytes(), []byte(fmt.Sprintf("-%d-%d", i, time.Now().UnixNano()))...))
		ids = append(ids, f.ID)
	}
	w.closeCase(t, w.occID, 60)
	require.Equal(t, fasthttp.StatusOK, w.setPolicy(t, w.admin, models.RetentionOccurrenceFiles, 12, true))
	return ids
}

func ledger(t *testing.T, w *retWorld, status string) int64 {
	t.Helper()
	var n int64
	q := w.app.DB.Model(&models.RetentionItem{}).Where("organization_id = ?", w.admin.OrganizationID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	q.Count(&n)
	return n
}

func TestRetentionEngine_ProductionCannotDeleteAndPlansOnlyItsLedger(t *testing.T) {
	assert.False(t, handlers.PurgeExecutionEnabledForTest, "the master switch is off in this release")
	w := newRetWorld(t)
	ids := w.oldFiles(t, 3)
	org := w.admin.OrganizationID
	store := newFakeStore()
	e := w.app.NewRetentionEngine(store) // the production constructor, even with a store

	_, err := e.ExecuteBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	assert.ErrorIs(t, err, handlers.ErrExecutionDisabled)
	assert.Zero(t, store.touched(), "nothing reached the store")

	res, err := e.PlanBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Equal(t, 3, res.Planned)
	for _, id := range ids {
		var n int64
		w.app.DB.Model(&models.OccurrenceFile{}).Where("id = ?", id).Count(&n)
		assert.EqualValues(t, 1, n, "no record of the product was touched")
		assert.Equal(t, fasthttp.StatusOK, w.dl(t, w.logPorto, id, ""))
	}
	// planning twice never queues a file twice
	res, err = e.PlanBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Zero(t, res.Planned)
	assert.EqualValues(t, 3, ledger(t, w, ""))
	assert.EqualValues(t, 3, e.Metrics(org)[models.ItemPlanned])
	assert.Zero(t, store.touched())
}

func TestRetentionEngine_PlansOnlyWhatIsEligible_DisabledPolicyPlansNothing(t *testing.T) {
	w := newRetWorld(t)
	org := w.admin.OrganizationID
	w.oldFiles(t, 1) // eligible
	// protected ones: a young protocol, a held one, a shared one, an infected one
	young := w.forward(t, nil)
	code, raw := w.fileUpload(t, w.logPorto, young, "jovem.pdf", append(pdfBytes(), 'j'))
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	w.closeCase(t, young, 1)
	held := w.forward(t, nil)
	code, raw = w.fileUpload(t, w.logPorto, held, "retido.pdf", append(pdfBytes(), 'r'))
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	w.closeCase(t, held, 60)
	number, _ := w.protocolOf(t, held)
	hcode, _ := w.placeHold(t, w.admin, map[string]any{"scope_type": "occurrence", "protocol_number": number, "reason": "Litígio em andamento com o cliente"})
	require.Equal(t, fasthttp.StatusOK, hcode)
	inf := w.forward(t, nil)
	code, raw = w.fileUpload(t, w.logPorto, inf, "infectado.pdf", append(pdfBytes(), 'i'))
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	w.closeCase(t, inf, 60)
	require.NoError(t, w.app.DB.Model(&models.FileScan{}).Where("resource_id = ?", uploaded(t, raw).File.ID).Update("status", models.ScanInfected).Error)

	e := w.app.NewRetentionEngine(nil)
	res, err := e.PlanBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Planned, "only the vencido, unprotected file")

	// switch the policy off: a new plan finds nothing and says why
	require.Equal(t, fasthttp.StatusOK, w.setPolicy(t, w.admin, models.RetentionOccurrenceFiles, 12, false))
	require.NoError(t, w.app.DB.Where("organization_id = ?", org).Delete(&models.RetentionItem{}).Error)
	res, err = e.PlanBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Zero(t, res.Planned)
	var run models.RetentionRun
	require.NoError(t, w.app.DB.Where("id = ?", res.RunID).First(&run).Error)
	assert.Equal(t, "policy_disabled", run.Counts["reason"])
}

func TestRetentionEngine_ExecutionRechecksProtectionsAtTheMomentOfRemoval(t *testing.T) {
	w := newRetWorld(t)
	org := w.admin.OrganizationID
	ids := w.oldFiles(t, 4)
	store := newFakeStore()
	e := w.app.NewRetentionEngineForTest(store)
	res, err := e.PlanBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	require.Equal(t, 4, res.Planned)

	// after planning, a hold is placed on the protocol: nothing may be removed
	number, _ := w.protocolOf(t, w.occID)
	hcode, hold := w.placeHold(t, w.admin, map[string]any{"scope_type": "occurrence", "protocol_number": number, "reason": "Litígio surgiu depois do planejamento"})
	require.Equal(t, fasthttp.StatusOK, hcode)
	out, err := e.ExecuteBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Equal(t, 4, out.Skipped)
	assert.Zero(t, out.Deleted)
	assert.Zero(t, store.touched(), "a file under legal hold never reaches the store")
	assert.EqualValues(t, 4, ledger(t, w, models.ItemSkipped))

	// revoked: replan (skipped items are not re-queued by the unique key, so reset them as a new cycle would)
	require.Equal(t, fasthttp.StatusOK, w.revokeHold(t, w.admin, hold, "Litígio encerrado depois do planejamento"))
	require.NoError(t, w.app.DB.Model(&models.RetentionItem{}).Where("organization_id = ?", org).Update("status", models.ItemPlanned).Error)

	// the policy is switched off before execution: nothing is removed either
	require.Equal(t, fasthttp.StatusOK, w.setPolicy(t, w.admin, models.RetentionOccurrenceFiles, 12, false))
	out, err = e.ExecuteBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Equal(t, 4, out.Skipped)
	assert.Zero(t, store.touched())

	// a file that became shared is skipped as well
	require.Equal(t, fasthttp.StatusOK, w.setPolicy(t, w.admin, models.RetentionOccurrenceFiles, 12, true))
	require.NoError(t, w.app.DB.Model(&models.RetentionItem{}).Where("organization_id = ?", org).Update("status", models.ItemPlanned).Error)
	var shared models.OccurrenceFile
	require.NoError(t, w.app.DB.First(&shared, "id = ?", ids[0]).Error)
	contact := testutil.CreateTestContact(t, w.app.DB, org)
	require.NoError(t, w.app.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, ContactID: contact.ID,
		Direction: models.DirectionIncoming, MessageType: models.MessageTypeDocument, MediaURL: shared.StoragePath, Status: models.MessageStatusDelivered}).Error)
	out, err = e.ExecuteBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Equal(t, 3, out.Deleted, "the three unshared files go")
	assert.Equal(t, 1, out.Skipped, "the shared one stays")
	assert.Equal(t, 0, store.records[ids[0]])
	assert.Zero(t, store.files[shared.StoragePath], "a file still referenced by another record is never removed")
}

func TestRetentionEngine_OrderIdempotencyAuditAndMetrics(t *testing.T) {
	w := newRetWorld(t)
	org := w.admin.OrganizationID
	ids := w.oldFiles(t, 3)
	store := newFakeStore()
	e := w.app.NewRetentionEngineForTest(store)
	_, err := e.PlanBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)

	out, err := e.ExecuteBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Equal(t, 3, out.Deleted)
	// the database record goes before its file, for every item
	pos := map[string]int{}
	for i, s := range store.order {
		pos[s] = i
	}
	var paths = map[uuid.UUID]string{}
	var rows []models.RetentionItem
	require.NoError(t, w.app.DB.Where("organization_id = ?", org).Find(&rows).Error)
	for _, r := range rows {
		paths[r.ResourceID] = r.StoragePath
	}
	for _, id := range ids {
		assert.Less(t, pos["record:"+id.String()], pos["file:"+paths[id]], "record first, then file")
	}
	// running it again does nothing (idempotent), and planning does not bring the same files back
	out, err = e.ExecuteBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Zero(t, out.Deleted+out.Skipped+out.Failed)
	for _, id := range ids {
		assert.Equal(t, 1, store.records[id], "each record was removed exactly once")
	}
	assert.EqualValues(t, 3, e.Metrics(org)[models.ItemDone])
	require.Eventually(t, func() bool {
		var n int64
		w.app.DB.Model(&models.AuditLog{}).Where("organization_id = ? AND resource_type = ?", org, "retention_item").Count(&n)
		return n == 3
	}, 3*time.Second, 50*time.Millisecond, "every removal is audited")
	var run models.RetentionRun
	require.NoError(t, w.app.DB.Where("organization_id = ? AND mode = ?", org, "execute").Order("started_at").First(&run).Error)
	assert.EqualValues(t, 3, run.Counts["deleted"])
}

func TestRetentionEngine_PartialFailuresAreRetriedWithoutRepeatingTheRecord(t *testing.T) {
	w := newRetWorld(t)
	org := w.admin.OrganizationID
	ids := w.oldFiles(t, 2)
	store := newFakeStore()
	e := w.app.NewRetentionEngineForTest(store)
	_, err := e.PlanBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	var rows []models.RetentionItem
	require.NoError(t, w.app.DB.Where("organization_id = ?", org).Order("created_at").Find(&rows).Error)
	byID := map[uuid.UUID]models.RetentionItem{}
	for _, r := range rows {
		byID[r.ResourceID] = r
	}
	store.failFile[byID[ids[0]].StoragePath] = 1 // the disk fails once for the first file
	store.failRecord[ids[1]] = 1                 // the database fails once for the second

	out, err := e.ExecuteBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Equal(t, 2, out.Failed)
	assert.Zero(t, out.Deleted)
	var a, b models.RetentionItem
	require.NoError(t, w.app.DB.First(&a, "resource_id = ?", ids[0]).Error)
	require.NoError(t, w.app.DB.First(&b, "resource_id = ?", ids[1]).Error)
	assert.Equal(t, models.ItemRecordGone, a.Status, "the record is gone, only the file is pending")
	assert.Equal(t, models.ItemFailed, b.Status, "nothing was removed for this one")
	assert.Equal(t, 1, a.Attempts)
	assert.NotEmpty(t, a.LastError)
	require.NotNil(t, a.NextAttemptAt, "retried later, with a back-off")
	assert.True(t, a.NextAttemptAt.After(time.Now()))

	// not yet due: nothing happens; once due, the retry finishes them
	out, err = e.ExecuteBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Zero(t, out.Deleted+out.Failed)
	require.NoError(t, w.app.DB.Model(&models.RetentionItem{}).Where("organization_id = ?", org).Update("next_attempt_at", time.Now().Add(-time.Second)).Error)
	out, err = e.ExecuteBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Equal(t, 2, out.Deleted)
	assert.Equal(t, 1, out.Retried, "the first one only repeated the file")
	assert.Equal(t, 1, store.records[ids[0]], "its record was NOT removed a second time")
	assert.Equal(t, 2, store.records[ids[1]], "the second one's record was retried after its failure")
	assert.EqualValues(t, 2, e.Metrics(org)[models.ItemDone])
}

func TestRetentionEngine_ConcurrentWorkersTakeEachItemOnce(t *testing.T) {
	w := newRetWorld(t)
	org := w.admin.OrganizationID
	ids := w.oldFiles(t, 24)
	store := newFakeStore()
	plan := w.app.NewRetentionEngineForTest(store)
	plan.Batch = 100
	_, err := plan.PlanBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		e := w.app.NewRetentionEngineForTest(store)
		e.Batch = 5
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				_, _ = e.ExecuteBatch(context.Background(), org, models.RetentionOccurrenceFiles)
			}
		}()
	}
	wg.Wait()
	for _, id := range ids {
		assert.Equal(t, 1, store.records[id], "no item was removed twice by competing workers")
	}
	assert.EqualValues(t, 24, ledger(t, w, models.ItemDone))
}

func TestRetentionEngine_PauseResumeAndOrganizationIsolation(t *testing.T) {
	w := newRetWorld(t)
	org := w.admin.OrganizationID
	w.oldFiles(t, 2)
	store := newFakeStore()
	e := w.app.NewRetentionEngineForTest(store)
	_, err := e.PlanBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)

	require.NoError(t, e.PauseRuns(org))
	out, err := e.ExecuteBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Zero(t, out.Deleted, "a paused run is not executed")
	assert.Zero(t, store.touched())
	require.NoError(t, e.ResumeRuns(org))

	// another organization's engine pass never touches this organization's items
	other := newIsoWorld(t)
	out, err = e.ExecuteBatch(context.Background(), other.org.ID, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Zero(t, out.Deleted)
	assert.Zero(t, store.touched())

	out, err = e.ExecuteBatch(context.Background(), org, models.RetentionOccurrenceFiles)
	require.NoError(t, err)
	assert.Equal(t, 2, out.Deleted)
	assert.True(t, store.deletedOrgs[org])
	assert.Len(t, store.deletedOrgs, 1, "only this organization's records")
}

// The (unwired) filesystem store: removes only inside the storage root, treats a missing file as done.
func TestRetentionEngine_FilesystemStoreStaysInsideTheStorageRoot(t *testing.T) {
	w := newRetWorld(t)
	store := w.app.FSPurgeStoreForTest()
	root := w.app.Config.Storage.LocalPath
	require.NoError(t, os.MkdirAll(filepath.Join(root, "scratch"), 0o755))
	inside := filepath.Join(root, "scratch", "tmp.bin")
	require.NoError(t, os.WriteFile(inside, []byte("x"), 0o644))
	outside := filepath.Join(t.TempDir(), "must-survive.txt")
	require.NoError(t, os.WriteFile(outside, []byte("keep"), 0o644))

	assert.NoError(t, store.DeleteFile(context.Background(), filepath.Join("scratch", "tmp.bin")))
	_, err := os.Stat(inside)
	assert.True(t, os.IsNotExist(err))
	assert.NoError(t, store.DeleteFile(context.Background(), filepath.Join("scratch", "tmp.bin")), "already gone is success")
	assert.Error(t, store.DeleteFile(context.Background(), filepath.Join("..", filepath.Base(filepath.Dir(outside)), "must-survive.txt")), "a path that escapes the root is refused")
	_, err = os.Stat(outside)
	assert.NoError(t, err, "a file outside the root is untouched")
}
