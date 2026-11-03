package handlers

import (

	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
)

type forwardTarget struct {
	ID       uuid.UUID  `json:"id"`
	Name     string     `json:"name"`
	UnitID   *uuid.UUID `json:"unit_id,omitempty"`
	UnitName string     `json:"unit_name,omitempty"`
}

// ListForwardTargets lists the teams a case may be forwarded to. Anyone who can
// open cases needs it, and the regular teams listing only shows a user their
// own teams, so this returns just what the picker needs (team and store names)
// and nothing about members.
func (a *App) ListForwardTargets(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	// ?team_id=: the active people of that team, for choosing who receives a forwarded case
	if tid := string(r.RequestCtx.QueryArgs().Peek("team_id")); tid != "" {
		id, perr := uuid.Parse(tid)
		if perr != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid team_id", nil, "")
		}
		type member struct {
			ID       uuid.UUID `json:"id"`
			FullName string    `json:"full_name"`
		}
		var ms []member
		a.DB.Table("users u").Select("u.id AS id, u.full_name AS full_name").
			Joins("JOIN team_members tm ON tm.user_id = u.id AND tm.team_id = ?", id).
			Joins("JOIN teams t ON t.id = tm.team_id AND t.organization_id = ?", orgID).
			Where("u.organization_id = ? AND u.is_active = ? AND u.deleted_at IS NULL", orgID, true).Order("u.full_name").Scan(&ms)
		return r.SendEnvelope(map[string]any{"members": ms})
	}
	var teams []models.Team
	a.DB.Where("organization_id = ? AND is_active = ?", orgID, true).Order("name").Find(&teams)
	units := map[uuid.UUID]string{}
	var us []models.Unit
	a.DB.Select("id, name").Where("organization_id = ?", orgID).Find(&us)
	for _, u := range us {
		units[u.ID] = u.Name
	}
	out := make([]forwardTarget, 0, len(teams))
	for _, t := range teams {
		if a.isCommercialTeam(orgID, &t) {
			continue // sales teams are never offered as a protocol destination
		}
		ft := forwardTarget{ID: t.ID, Name: t.Name, UnitID: t.UnitID}
		if t.UnitID != nil {
			ft.UnitName = units[*t.UnitID]
		}
		out = append(out, ft)
	}
	return r.SendEnvelope(map[string]any{"targets": out})
}
