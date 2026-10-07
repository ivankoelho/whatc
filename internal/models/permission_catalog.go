package models

// Functional grouping of the permission catalog. It is presentation metadata
// only: the permission key ("resource:action") stays the authority and nothing
// here is read by the authorization path. Labels and descriptions live in the
// frontend i18n (en + pt-BR); the backend owns the structure and the order.

// PermissionGroup is one functional area of the permissions screen.
type PermissionGroup struct {
	Key       string
	Order     int
	Resources []string // in display order
}

// PermissionGroupOther receives any seeded resource not mapped below, so a key
// can never vanish from the screen.
const PermissionGroupOther = "other"

var permissionGroups = []PermissionGroup{
	{"admin", 10, []string{ResourceUsers, ResourceTeams, ResourceRoles, ResourceOrganizations, ResourceAuditLogs, ResourceSettingsGeneral, ResourceSettingsSSO}},
	{"service", 20, []string{ResourceChat, ResourceChatAssign, ResourceConversations, ResourceContacts, ResourceContactName, ResourceTags, ResourceTransfers, ResourceCannedResponses}},
	{"crm", 30, []string{ResourceOccurrences, ResourceOccurrenceStages, ResourceOccurrenceCategories, ResourceOccurrenceWhatHappened, ResourceOccurrenceSLAPolicies, ResourceOccurrenceProcesses, ResourceUnits, ResourceDepartments}},
	{"sales", 40, []string{ResourceSalesOpportunities}},
	{"whatsapp", 50, []string{ResourceAccounts, ResourceTemplates, ResourceCampaigns, ResourceFlowsWhatsApp, ResourceFlowsChatbot, ResourceChatbotKeywords, ResourceSettingsChatbot}},
	{"ai", 60, []string{ResourceChatbotAI, ResourceKnowledge, ResourceAITools}},
	{"integrations", 70, []string{ResourceXProcessIntegration, ResourceWebhooks, ResourceAPIKeys, ResourceCustomActions}},
	{"analytics", 80, []string{ResourceAnalytics, ResourceAnalyticsAgents, ResourceWhatsAppUsage}},
	{"calls", 90, []string{ResourceCallLogs, ResourceIVRFlows, ResourceCallTransfers, ResourceOutgoingCalls}},
}

// permissionActionOrder ranks actions inside a resource: reading first, then
// writing, deleting, then the specific ones.
var permissionActionOrder = []string{
	ActionRead, ActionWrite, ActionDelete, ActionSync, ActionExecute, ActionImport,
	ActionExport, ActionPickup, ActionAssign, ActionViewAll, ActionViewTeam,
}

// PermissionPresentation returns the functional group, its order and the sort
// position of a permission. sortOrder is global: group, then resource, then action.
func PermissionPresentation(resource, action string) (group string, groupOrder, sortOrder int) {
	group, groupOrder, resIdx := PermissionGroupOther, 999, 0
	for _, g := range permissionGroups {
		for i, r := range g.Resources {
			if r == resource {
				group, groupOrder, resIdx = g.Key, g.Order, i
			}
		}
	}
	actIdx := len(permissionActionOrder)
	for i, a := range permissionActionOrder {
		if a == action {
			actIdx = i
			break
		}
	}
	return group, groupOrder, groupOrder*10000 + resIdx*100 + actIdx
}
