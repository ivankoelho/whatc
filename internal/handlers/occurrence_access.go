package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// Occurrence access is decided by the case's OWN scope (assignee, opener, team,
// unit, participants) — never by who can see the originating conversation. A
// case forwarded from SAC to a store's Logistics team must be visible to that
// team and invisible to every other store, whatever the SAC chat looks like.
//
// Legacy cases with neither team nor unit are therefore restricted: only their
// assignee, opener, participants and conversations:view_all holders reach them.
// Nothing guesses a scope for old rows (see docs/superpowers/specs).
//
// visibleOccurrences (SQL) and authorizeOccurrence (Go) are twins: keep them in
// sync (TestOccurrenceScopeSQLMatchesGo).

type occurrenceAccess struct {
	view       bool
	interact   bool
	supervisor bool // may reassign / override a case that is not theirs
}

func (a *App) myTeamIDs(userID uuid.UUID) *gorm.DB {
	return a.DB.Model(&models.TeamMember{}).Select("team_id").Where("user_id = ?", userID)
}

// visibleOccurrences scopes a query on the occurrences table to what the user
// may see. Organization filtering stays with the caller.
func (a *App) visibleOccurrences(query *gorm.DB, userID, orgID uuid.UUID) *gorm.DB {
	if a.HasPermission(userID, models.ResourceConversations, models.ActionViewAll, orgID) {
		return query
	}
	cond := "occurrences.assigned_user_id = ? OR occurrences.opened_by_user_id = ? OR occurrences.team_id IN (?) OR occurrences.id IN (?)"
	args := []any{
		userID, userID, a.myTeamIDs(userID),
		a.DB.Model(&models.OccurrenceParticipant{}).Select("occurrence_id").
			Where("user_id = ? AND organization_id = ?", userID, orgID),
	}
	if a.HasPermission(userID, models.ResourceConversations, models.ActionViewTeam, orgID) {
		cond += " OR occurrences.unit_id IN (?) OR occurrences.assigned_user_id IN (?)"
		args = append(args,
			a.DB.Model(&models.Team{}).Select("unit_id").Where("id IN (?) AND unit_id IS NOT NULL", a.myTeamIDs(userID)),
			a.DB.Model(&models.TeamMember{}).Select("user_id").Where("team_id IN (?)", a.myTeamIDs(userID)))
	}
	return query.Where("("+cond+")", args...)
}

// authorizeOccurrence is the Go twin of visibleOccurrences.
func (a *App) authorizeOccurrence(userID, orgID uuid.UUID, occ *models.Occurrence) occurrenceAccess {
	if occ.OrganizationID != orgID {
		return occurrenceAccess{}
	}
	if a.HasPermission(userID, models.ResourceConversations, models.ActionViewAll, orgID) {
		return occurrenceAccess{view: true, interact: true, supervisor: true}
	}
	if occ.AssignedUserID != nil && *occ.AssignedUserID == userID {
		return occurrenceAccess{view: true, interact: true}
	}

	var teams []uuid.UUID
	a.myTeamIDs(userID).Pluck("team_id", &teams)
	inTeam := false
	if occ.TeamID != nil {
		for _, t := range teams {
			if t == *occ.TeamID {
				inTeam = true
			}
		}
	}
	if inTeam {
		return occurrenceAccess{view: true, interact: true}
	}

	if a.HasPermission(userID, models.ResourceConversations, models.ActionViewTeam, orgID) && len(teams) > 0 {
		var n int64
		if occ.UnitID != nil {
			a.DB.Model(&models.Team{}).Where("id IN ? AND unit_id = ?", teams, *occ.UnitID).Count(&n)
		}
		if n == 0 && occ.AssignedUserID != nil {
			a.DB.Model(&models.TeamMember{}).Where("user_id = ? AND team_id IN ?", *occ.AssignedUserID, teams).Count(&n)
		}
		if n > 0 {
			return occurrenceAccess{view: true, interact: true, supervisor: true}
		}
	}

	acc := occurrenceAccess{}
	if occ.OpenedByUserID == userID {
		// The opener follows a forwarded case; they only act on it when it has
		// no team of its own to work it.
		acc.view, acc.interact = true, occ.TeamID == nil
	}
	if !acc.view {
		var p int64
		a.DB.Model(&models.OccurrenceParticipant{}).
			Where("occurrence_id = ? AND user_id = ?", occ.ID, userID).Count(&p)
		acc.view = p > 0
	}
	return acc
}

// CanViewOccurrenceByID authorizes WebSocket delivery of occurrence events.
func (a *App) CanViewOccurrenceByID(userID, orgID, occurrenceID uuid.UUID) bool {
	var occ models.Occurrence
	if err := a.DB.Where("id = ? AND organization_id = ?", occurrenceID, orgID).First(&occ).Error; err != nil {
		return false
	}
	return a.authorizeOccurrence(userID, orgID, &occ).view
}

// broadcastOccurrence delivers an occurrence event to exactly the users who
// pass authorizeOccurrence, in one pass.
func (a *App) broadcastOccurrence(orgID, occurrenceID uuid.UUID, msg websocket.WSMessage) {
	if a.WSHub == nil {
		return
	}
	a.WSHub.Broadcast(websocket.BroadcastMessage{OrgID: orgID, OccurrenceID: occurrenceID, Message: msg})
}

// loadAuthorizedOccurrence resolves the occurrence from the path and applies
// the case-scope rule. A case outside the user's scope answers 403 whether or
// not it exists in another organization's data: findByIDAndOrg already 404s
// foreign organizations.
func (a *App) loadAuthorizedOccurrence(r *fastglue.Request, orgID, userID uuid.UUID, needInteract bool) (*models.Occurrence, error) {
	occ, _, err := a.loadOccurrenceWithAccess(r, orgID, userID, needInteract)
	return occ, err
}

func (a *App) loadOccurrenceWithAccess(r *fastglue.Request, orgID, userID uuid.UUID, needInteract bool) (*models.Occurrence, occurrenceAccess, error) {
	occurrenceID, err := parsePathUUID(r, "id", "occurrence")
	if err != nil {
		return nil, occurrenceAccess{}, errEnvelopeSent
	}
	occ, err := findByIDAndOrg[models.Occurrence](a.DB, r, occurrenceID, orgID, "Occurrence")
	if err != nil {
		return nil, occurrenceAccess{}, errEnvelopeSent
	}
	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", occ.ContactID, orgID).
		First(&contact).Error; err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
		return nil, occurrenceAccess{}, errEnvelopeSent
	}
	acc := a.authorizeOccurrence(userID, orgID, occ)
	if (needInteract && !acc.interact) || (!needInteract && !acc.view) {
		_ = r.SendErrorEnvelope(fasthttp.StatusForbidden, "You do not have access to this occurrence", nil, "")
		return nil, occurrenceAccess{}, errEnvelopeSent
	}
	occ.Contact = &contact
	return occ, acc, nil
}
