package handlers

import (
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// errCommercialDestination is the refusal for a protocol aimed at a commercial team.
const errCommercialDestination = "Commercial teams (Vendas) cannot receive protocols: send it to SAC, Jurídico or the store supervision, or open an opportunity in the CRM"

// isCommercialTeam reports whether the team belongs to a department marked commercial. Protocols are internal
// cases for SAC, Jurídico, the store supervision and Logística support; sellers work in the CRM and the chat.
// The rule is the server's, so a direct API call cannot route around the picker.
func (a *App) isCommercialTeam(orgID uuid.UUID, team *models.Team) bool {
	if team == nil {
		return false
	}
	if team.Commercial {
		return true
	}
	if team.DepartmentID == nil {
		// no department to classify it: a team named like a sales team is one, so forgetting the flag is not an opening
		return commercialName(team.Name)
	}
	var n int64
	a.DB.Model(&models.Department{}).Where("id = ? AND organization_id = ? AND commercial = ?", *team.DepartmentID, orgID, true).Count(&n)
	return n > 0
}

// commercialName: Vendas / Comercial (and "Vendas Loja 1"...), ignoring case and accents on the first word.
func commercialName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(n, "vendas") || strings.HasPrefix(n, "venda ") || strings.HasPrefix(n, "comercial")
}

// isCommercialTeamID is isCommercialTeam for a team known only by id.
func (a *App) isCommercialTeamID(orgID, teamID uuid.UUID) bool {
	var team models.Team
	if a.DB.Where("id = ? AND organization_id = ?", teamID, orgID).First(&team).Error != nil {
		return false
	}
	return a.isCommercialTeam(orgID, &team)
}
