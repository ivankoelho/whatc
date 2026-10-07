package usage

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// The recording has two switches. The SERVER switch is usage.record_enabled in the
// configuration (a hard off: nothing is recorded and the panel cannot turn it on).
// The PANEL switch is per organization, kept in organizations.settings, and an
// administrator flips it from the consumption screen. Absent means ON, so nothing
// changes for an organization that never touched it. Recording happens only when
// both are on.
//
// Turning the panel switch off stops everything for that organization: no new rows,
// events or status history, and the integrity job leaves it alone, so existing rows
// are frozen as they are. Turning it on again resumes from that instant: what
// happened while it was off is never reconstructed (the integrity job only looks at
// messages created after the last time it was turned on).

// RecordingSettingsKey is the key of organizations.settings that holds the panel switch.
const RecordingSettingsKey = "whatsapp_usage_recording"

// recordingCacheTTL bounds how long another server instance can keep acting on an old
// value of the panel switch. A technical bound (the instance that flips it updates its
// own cache at once), not an operational parameter.
const recordingCacheTTL = 15 * time.Second

// RecordingState is the panel switch of one organization.
type RecordingState struct {
	Enabled   bool       `json:"enabled"`
	ChangedAt *time.Time `json:"changed_at,omitempty"` // when it was last flipped; nil = never
}

type recordingEntry struct {
	enabled bool
	until   time.Time
}

// readRecordingState reads the panel switch from the database.
func (r *Recorder) readRecordingState(ctx context.Context, orgID uuid.UUID) (RecordingState, error) {
	var org models.Organization
	if err := r.DB.WithContext(ctx).Select("id", "settings").Where("id = ?", orgID).Take(&org).Error; err != nil {
		return RecordingState{Enabled: true}, err
	}
	st := RecordingState{Enabled: true}
	raw, _ := org.Settings[RecordingSettingsKey].(map[string]any)
	if v, ok := raw["enabled"].(bool); ok {
		st.Enabled = v
	}
	if s, ok := raw["changed_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			st.ChangedAt = &t
		}
	}
	return st, nil
}

// RecordingState returns the panel switch of an organization (default: on).
func (r *Recorder) RecordingState(ctx context.Context, orgID uuid.UUID) (RecordingState, error) {
	if r == nil || r.DB == nil {
		return RecordingState{Enabled: true}, nil
	}
	return r.readRecordingState(ctx, orgID)
}

// EnabledFor says whether anything may be recorded for the organization: the server
// switch AND the panel switch. A failure to read the panel switch keeps the last known
// value (or "on", the default, when there is none): reading it must never break a send.
func (r *Recorder) EnabledFor(ctx context.Context, orgID uuid.UUID) bool {
	if !r.Enabled() {
		return false
	}
	now := time.Now()
	r.mu.Lock()
	e, ok := r.cache[orgID]
	r.mu.Unlock()
	if ok && now.Before(e.until) {
		return e.enabled
	}
	st, err := r.readRecordingState(ctx, orgID)
	if err != nil {
		if ok {
			return e.enabled
		}
		return true
	}
	r.remember(orgID, st.Enabled)
	return st.Enabled
}

func (r *Recorder) remember(orgID uuid.UUID, enabled bool) {
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[uuid.UUID]recordingEntry{}
	}
	r.cache[orgID] = recordingEntry{enabled: enabled, until: time.Now().Add(recordingCacheTTL)}
	r.mu.Unlock()
}

// SetRecording flips the panel switch of an organization and takes effect at once in
// this instance (others follow within recordingCacheTTL).
func (r *Recorder) SetRecording(ctx context.Context, orgID uuid.UUID, enabled bool, now time.Time) (RecordingState, error) {
	err := r.DB.WithContext(ctx).Exec(`
		UPDATE organizations
		SET settings = COALESCE(settings, '{}'::jsonb) || jsonb_build_object(?::text, jsonb_build_object('enabled', ?::boolean, 'changed_at', ?::text))
		WHERE id = ?`, RecordingSettingsKey, enabled, now.UTC().Format(time.RFC3339Nano), orgID).Error
	if err != nil {
		return RecordingState{}, err
	}
	r.remember(orgID, enabled)
	t := now.UTC()
	return RecordingState{Enabled: enabled, ChangedAt: &t}, nil
}
