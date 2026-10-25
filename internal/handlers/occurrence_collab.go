package handlers

import (

	"github.com/google/uuid"
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
		ft := forwardTarget{ID: t.ID, Name: t.Name, UnitID: t.UnitID}
		if t.UnitID != nil {
			ft.UnitName = units[*t.UnitID]
		}
		out = append(out, ft)
	}
	return r.SendEnvelope(map[string]any{"targets": out})
}
