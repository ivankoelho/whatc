package handlers

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm/clause"
)

// IVR audio played into calls.
//
// Every playback goes through calling.AudioPlayer.PlayFile, which asks this gate. The gate answers from
// an in-memory set of file names that have a clean verdict: no disk, no database and no scan happen
// during a call. The set is prepared ahead of time:
//   - uploads are scanned before ffmpeg and recorded clean (recordIngressClean also adds to the set);
//   - older files (no row) are registered by the worker from a listing of the audio directory and
//     scanned in the background; once clean they join the set;
//   - the set is rebuilt from the verdicts every worker pass, so a file that is quarantined, or whose
//     verdict is withdrawn, leaves it within seconds.
//
// A file that is not in the set is simply not played: the flow continues in silence at that point.

var orgAudioName = regexp.MustCompile(`^org_([0-9a-fA-F-]{36})_(hold_music|ringback)\.ogg$`)

// AudioGate is the calling.AudioGate of the server.
type AudioGate struct {
	a *App

	mu       sync.RWMutex
	approved map[string]bool // base file name -> approved
}

// AudioGate returns the gate (one per App).
func (a *App) AudioGate() *AudioGate {
	a.audioGateOnce.Do(func() { a.audioGate = &AudioGate{a: a, approved: map[string]bool{}} })
	return a.audioGate
}

// Approved: inside the audio directory, and with a clean verdict. Memory only.
func (g *AudioGate) Approved(path string) bool {
	dir, err := filepath.Abs(g.a.getAudioDir())
	if err != nil {
		return false
	}
	full, err := filepath.Abs(path)
	if err != nil || filepath.Dir(full) != dir { // outside (or deeper than) the audio directory: never
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.approved[filepath.Base(full)]
}

func (g *AudioGate) add(name string) {
	g.mu.Lock()
	g.approved[name] = true
	g.mu.Unlock()
}

// Refresh rebuilds the approved set from the verdicts (clean rows only).
func (g *AudioGate) Refresh() {
	var paths []string
	if err := g.a.DB.Model(&models.FileScan{}).Where("source = ? AND status = ?", models.ScanSourceIVRAudio, models.ScanClean).
		Pluck("storage_path", &paths).Error; err != nil {
		g.a.Log.Error("Audio gate refresh failed; keeping the previous approvals", "error", err)
		return
	}
	next := make(map[string]bool, len(paths))
	for _, p := range paths {
		next[filepath.Base(p)] = true
	}
	g.mu.Lock()
	g.approved = next
	g.mu.Unlock()
}

// registerAudioDir queues the files of the audio directory that have no verdict yet (older uploads,
// operator-provided defaults). Bounded per pass. Files named for an organization belong to it; the rest
// belong to the system (org uuid.Nil), which every organization's editor may preview as before.
func (a *App) registerAudioDir(limit int) {
	entries, err := os.ReadDir(a.getAudioDir())
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(strings.ToLower(e.Name()), ".ogg") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return
	}
	var have []string
	a.DB.Model(&models.FileScan{}).Where("source = ?", models.ScanSourceIVRAudio).Pluck("storage_path", &have)
	known := make(map[string]bool, len(have))
	for _, h := range have {
		known[filepath.Base(h)] = true
	}
	n := 0
	now := time.Now()
	for _, name := range names {
		if known[name] || n >= limit {
			continue
		}
		org := uuid.Nil
		if m := orgAudioName.FindStringSubmatch(name); m != nil {
			if id, perr := uuid.Parse(m[1]); perr == nil {
				org = id
			}
		}
		fi, _ := os.Stat(filepath.Join(a.getAudioDir(), name))
		var size int64
		if fi != nil {
			size = fi.Size()
		}
		_ = a.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.FileScan{
			ID: uuid.New(), OrganizationID: org, Source: models.ScanSourceIVRAudio, ResourceID: fixedScanID("ivr", name),
			StoragePath: name, SizeBytes: size, Status: models.ScanPending, NextAttemptAt: &now,
		}).Error
		n++
	}
}
