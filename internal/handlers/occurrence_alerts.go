package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func sameOptUUID(x, y *uuid.UUID) bool {
	if x == nil || y == nil {
		return x == y
	}
	return *x == *y
}

// alertOccurrence records who must hear that a protocol landed somewhere: the
// new assignee (when it is not the actor) and the managers of the case's team.
// Called only where ownership really changes (opening, reassignment, forward);
// a protocol that is merely open is never an alert. Failures are logged, never
// fatal: an alert must not block the operation that caused it.
func (a *App) alertOccurrence(occ *models.Occurrence, assignee *uuid.UUID, actor uuid.UUID, kind string) {
	recipients := map[uuid.UUID]string{}
	if assignee != nil && *assignee != actor {
		recipients[*assignee] = kind
	}
	if occ.TeamID != nil {
		var managers []uuid.UUID
		a.DB.Model(&models.TeamMember{}).
			Where("team_id = ? AND role = ?", *occ.TeamID, models.TeamRoleManager).
			Pluck("user_id", &managers)
		for _, m := range managers {
			if _, taken := recipients[m]; !taken && m != actor {
				recipients[m] = "team"
			}
		}
	}
	ids := make([]uuid.UUID, 0, len(recipients))
	for uid, k := range recipients {
		// Idempotent: a repeated event must not stack a second pending alert for
		// the same person and protocol.
		var pending int64
		a.DB.Model(&models.OccurrenceAlert{}).
			Where("occurrence_id = ? AND user_id = ? AND dismissed_at IS NULL", occ.ID, uid).Count(&pending)
		if pending > 0 {
			continue
		}
		n := models.OccurrenceAlert{OrganizationID: occ.OrganizationID, UserID: uid, OccurrenceID: occ.ID, Kind: k, ActorUserID: &actor}
		if err := a.DB.Create(&n).Error; err != nil {
			a.Log.Error("Failed to create occurrence alert", "error", err)
			continue
		}
		ids = append(ids, uid)
	}
	if a.WSHub != nil && len(ids) > 0 {
		a.WSHub.BroadcastToUsers(occ.OrganizationID, ids, websocket.WSMessage{Type: websocket.TypeOccurrenceAlert})
	}
}

// clearOccurrenceAlerts dismisses every pending alert of a protocol that is
// being handed over: previous holders no longer own it, and the new round is
// created right after.
func (a *App) clearOccurrenceAlerts(occurrenceID uuid.UUID) {
	a.DB.Model(&models.OccurrenceAlert{}).
		Where("occurrence_id = ? AND dismissed_at IS NULL", occurrenceID).
		Update("dismissed_at", time.Now())
}

// OccurrenceAlertItem is the API shape of one alert.
type OccurrenceAlertItem struct {
	ID             uuid.UUID  `json:"id"`
	Kind           string     `json:"kind"`
	CreatedAt      time.Time  `json:"created_at"`
	SeenAt         *time.Time `json:"seen_at,omitempty"`
	OccurrenceID   uuid.UUID  `json:"occurrence_id"`
	ProtocolNumber string     `json:"protocol_number"`
	Title          string     `json:"title"`
	ContactName    string     `json:"contact_name"`
	CategoryName   string     `json:"category_name,omitempty"`
	WhatHappened   string     `json:"what_happened,omitempty"`
	Priority       string     `json:"priority"`
	OpenedAt       time.Time  `json:"opened_at"`
	ActorName      string     `json:"actor_name,omitempty"`
}

// ListOccurrenceAlerts returns the caller's undismissed alerts for protocols
// that are still open and still inside the caller's visibility (the same scope
// as the protocol list: organization, team, unit, participants).
func (a *App) ListOccurrenceAlerts(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	var alerts []models.OccurrenceAlert
	visible := a.visibleOccurrences(a.DB.Model(&models.Occurrence{}).Select("occurrences.id").
		Where("occurrences.organization_id = ? AND occurrences.closed_at IS NULL", orgID), userID, orgID)
	if err := a.DB.Where("organization_id = ? AND user_id = ? AND dismissed_at IS NULL", orgID, userID).
		Where("occurrence_id IN (?)", visible).
		Order("created_at DESC").Limit(50).Find(&alerts).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load alerts", nil, "")
	}

	items := make([]OccurrenceAlertItem, 0, len(alerts))
	unseen := 0
	for _, n := range alerts {
		var occ models.Occurrence
		if a.DB.Preload("Contact").Preload("Category").Preload("WhatHappened").First(&occ, "id = ?", n.OccurrenceID).Error != nil {
			continue
		}
		it := OccurrenceAlertItem{ID: n.ID, Kind: n.Kind, CreatedAt: n.CreatedAt, SeenAt: n.SeenAt,
			OccurrenceID: occ.ID, ProtocolNumber: occ.ProtocolNumber, Title: occ.Title,
			Priority: string(occ.Priority), OpenedAt: occ.OpenedAt}
		if occ.Contact != nil {
			it.ContactName = occ.Contact.ProfileName
		}
		if occ.Category != nil {
			it.CategoryName = occ.Category.Name
		}
		if occ.WhatHappened != nil {
			it.WhatHappened = occ.WhatHappened.Name
		}
		if n.ActorUserID != nil {
			var u models.User
			if a.DB.Select("full_name").First(&u, "id = ?", *n.ActorUserID).Error == nil {
				it.ActorName = u.FullName
			}
		}
		if n.SeenAt == nil {
			unseen++
		}
		items = append(items, it)
	}
	return r.SendEnvelope(map[string]any{"alerts": items, "unseen": unseen})
}

// DismissOccurrenceAlert dismisses one of the caller's alerts.
func (a *App) DismissOccurrenceAlert(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "alert")
	if err != nil {
		return nil
	}
	res := a.DB.Model(&models.OccurrenceAlert{}).
		Where("id = ? AND organization_id = ? AND user_id = ?", id, orgID, userID).
		Update("dismissed_at", time.Now())
	if res.RowsAffected == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Alert not found", nil, "")
	}
	return r.SendEnvelope(map[string]string{"status": "dismissed"})
}

// DismissAllOccurrenceAlerts dismisses everything pending for the caller.
func (a *App) DismissAllOccurrenceAlerts(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	a.DB.Model(&models.OccurrenceAlert{}).
		Where("organization_id = ? AND user_id = ? AND dismissed_at IS NULL", orgID, userID).
		Update("dismissed_at", time.Now())
	return r.SendEnvelope(map[string]string{"status": "dismissed"})
}

// MarkOccurrenceAlertsSeen flips the caller's unseen alerts to seen.
func (a *App) MarkOccurrenceAlertsSeen(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	a.DB.Model(&models.OccurrenceAlert{}).
		Where("organization_id = ? AND user_id = ? AND seen_at IS NULL", orgID, userID).
		Update("seen_at", time.Now())
	return r.SendEnvelope(map[string]string{"status": "seen"})
}
