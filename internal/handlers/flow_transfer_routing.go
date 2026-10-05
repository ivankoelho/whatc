package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// Flow transfer destination (transfer node config "destination"):
//
//	absent or "team"      the node's fixed team_id, or the general queue (the original behavior)
//	"contact_placement"   the team tagged with the contact's unit + department
const flowTransferDestinationPlacement = "contact_placement"

// resolveContactPlacementTeam finds the team a contact's unit + department point to. The pair is
// read from the contact row (never from the conversation), only a colaborador has one (the server
// enforces it) and BOTH fields are required. It returns the team and RoutingReasonContactPlacement,
// or no team and why there is none:
//
//	not a colaborador, or unit or department missing  -> no_contact_placement
//	no active team tagged with the pair               -> no_team_for_contact_placement
//	more than one active team tagged with the pair    -> ambiguous_contact_placement
//
// With more than one team nothing is chosen: the distribution only exists inside a team and the
// choice between teams would depend on the moment and on query order. A duplicated pair is a
// configuration the node answers with its fallback.
func (a *App) resolveContactPlacementTeam(contact *models.Contact) (uuid.UUID, string) {
	if contact.ContactType != models.ContactTypeColaborador || contact.UnitID == nil || contact.DepartmentID == nil {
		return uuid.Nil, models.RoutingReasonNoContactPlacement
	}
	var ids []uuid.UUID
	err := a.DB.Model(&models.Team{}).
		Where("organization_id = ? AND unit_id = ? AND department_id = ? AND is_active = ?",
				contact.OrganizationID, *contact.UnitID, *contact.DepartmentID, true).
		Limit(2). // two are enough to know it is ambiguous
		Pluck("id", &ids).Error
	if err != nil {
		// not a fifth reason: the node falls back, and the cause is in the log
		a.Log.Error("Flow transfer: could not look up the team of the contact's placement", "error", err, "contact_id", contact.ID)
		return uuid.Nil, models.RoutingReasonNoTeamForPlacement
	}
	switch len(ids) {
	case 0:
		return uuid.Nil, models.RoutingReasonNoTeamForPlacement
	case 1:
		return ids[0], models.RoutingReasonContactPlacement
	default:
		return uuid.Nil, models.RoutingReasonAmbiguousPlacement
	}
}

// routeFlowTransfer carries out a flow transfer node: the fixed team or the queue as always, or,
// with destination "contact_placement", the team of the contact's unit + department with the
// node's fallback when there is none. Whatever happens, the contact is transferred somewhere, and
// the reason is recorded on the transfer and in the log.
func (a *App) routeFlowTransfer(node *ChatNode, ctx *chatNodeCtx, notes string) {
	destination := stringFromConfig(node.Config, "destination")
	if destination != "" && destination != "team" && destination != flowTransferDestinationPlacement {
		a.Log.Warn("transfer node has an unknown destination, using the fixed team", "node", node.ID, "destination", destination)
		destination = "team"
	}

	if destination == flowTransferDestinationPlacement {
		teamID, reason := a.resolveContactPlacementTeam(ctx.contact)
		if reason == models.RoutingReasonContactPlacement {
			a.Log.Info("Flow transfer routed by the contact's placement",
				"node", node.ID, "contact_id", ctx.contact.ID, "team_id", teamID, "routing_reason", reason)
			a.createTransferToTeam(ctx.account, ctx.contact, teamID, notes, models.TransferSourceFlow, reason)
			return
		}
		a.Log.Info("Flow transfer by the contact's placement uses the fallback",
			"node", node.ID, "contact_id", ctx.contact.ID, "routing_reason", reason)
		a.transferToFallback(node, ctx, notes, reason)
		return
	}

	teamIDStr := stringFromConfig(node.Config, "team_id")
	if teamIDStr != "" && teamIDStr != "_general" {
		if parsed, err := uuid.Parse(teamIDStr); err == nil {
			a.createTransferToTeam(ctx.account, ctx.contact, parsed, notes, models.TransferSourceFlow, models.RoutingReasonTeamFixed)
			return
		}
		a.Log.Warn("transfer node has invalid team_id, falling back to queue",
			"node", node.ID, "team_id", teamIDStr)
	}
	a.createTransferToQueueRouted(ctx.account, ctx.contact, models.TransferSourceFlow, models.RoutingReasonTeamFixed)
}

// transferToFallback sends the contact to the node's fallback_team_id (a live team of the contact's
// organization) or, without one or when it is not valid, to the general queue.
func (a *App) transferToFallback(node *ChatNode, ctx *chatNodeCtx, notes, reason string) {
	if fb := stringFromConfig(node.Config, "fallback_team_id"); fb != "" && fb != "_general" {
		if parsed, err := uuid.Parse(fb); err == nil {
			var n int64
			a.DB.Model(&models.Team{}).
				Where("id = ? AND organization_id = ? AND is_active = ?", parsed, ctx.contact.OrganizationID, true).
				Count(&n)
			if n > 0 {
				a.createTransferToTeam(ctx.account, ctx.contact, parsed, notes, models.TransferSourceFlow, reason)
				return
			}
		}
		a.Log.Warn("transfer node fallback_team_id is not an active team of the organization, using the queue",
			"node", node.ID, "fallback_team_id", fb)
	}
	a.createTransferToQueueRouted(ctx.account, ctx.contact, models.TransferSourceFlow, reason)
}
