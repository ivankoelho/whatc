package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// enrichOpportunities fills what the board card shows besides the opportunity itself: the Meta ad that brought the
// customer and the meeting the AI agent booked. It reads both for the whole page in two queries (never one per
// card) and writes into the slice it is given, so callers that send the slice send the context with it.
func (a *App) enrichOpportunities(orgID uuid.UUID, opps []models.SalesOpportunity) {
	if len(opps) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(opps))
	seen := map[uuid.UUID]bool{}
	for _, o := range opps {
		if !seen[o.ContactID] {
			seen[o.ContactID] = true
			ids = append(ids, o.ContactID)
		}
	}

	origins := map[uuid.UUID]*models.OpportunityAdOrigin{}
	var refs []models.ContactAdReferral
	if err := a.DB.Where("organization_id = ? AND contact_id IN ?", orgID, ids).Order("created_at ASC").Find(&refs).Error; err == nil {
		for _, r := range refs {
			if o, ok := origins[r.ContactID]; ok {
				o.Clicks++
				continue
			}
			origins[r.ContactID] = &models.OpportunityAdOrigin{Headline: r.Headline, SourceID: r.SourceID, Clicks: 1}
		}
	}

	meetings := map[uuid.UUID]*models.OpportunityMeeting{}
	var offers []models.AIAppointmentOffer
	if err := a.DB.Where("organization_id = ? AND contact_id IN ? AND status = ? AND booked_start > ?",
		orgID, ids, models.AIAppointmentBooked, time.Now()).Order("booked_start ASC").Find(&offers).Error; err == nil && len(offers) > 0 {
		var cal models.NextcloudCalendar
		location := ""
		if a.DB.Select("location").Where("organization_id = ?", orgID).First(&cal).Error == nil {
			location = cal.Location
		}
		for _, o := range offers {
			if _, ok := meetings[o.ContactID]; !ok && o.BookedStart != nil {
				meetings[o.ContactID] = &models.OpportunityMeeting{Start: *o.BookedStart, Location: location}
			}
		}
	}

	for i := range opps {
		opps[i].AdOrigin = origins[opps[i].ContactID]
		opps[i].Meeting = meetings[opps[i].ContactID]
	}
}
