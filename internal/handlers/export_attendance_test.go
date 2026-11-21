package handlers

import (
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
)

// SessionsForTest exposes sessionsFrom to the external test package.
func SessionsForTest(e []models.UserSessionEvent, s, en, now time.Time, life time.Duration) ([]AttendanceSession, *time.Time, int) {
	return sessionsFrom(e, s, en, now, life)
}
