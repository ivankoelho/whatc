package models

import "strings"

// TeamProfile is a preset, editable role for one operational team. Unlike the
// admin/manager/agent system roles it is created once per organization when
// missing (by name) and never overwritten, so admins can tune it afterwards.
type TeamProfile struct {
	Name        string
	Description string
	Permissions []string // "resource:action"
}

// TeamProfiles returns the SAC / Vendas / Logística / Supervisão presets. Each
// one starts from the agent baseline and adds only what the team needs.
func TeamProfiles() []TeamProfile {
	// The agent baseline carries sales_opportunities; only Vendas and the
	// supervisors should see the funnel, so strip it and add it back per profile.
	var base []string
	for _, k := range SystemRolePermissions()["agent"] {
		if !strings.HasPrefix(k, "sales_opportunities:") {
			base = append(base, k)
		}
	}
	with := func(extra ...string) []string {
		return append(append([]string{}, base...), extra...)
	}

	return []TeamProfile{
		{
			Name:        "SAC",
			Description: "Atendimento ao cliente: recebe a conversa após o fluxo e a transfere para Vendas ou Logística",
			Permissions: with("contacts:read", "tags:read", "templates:read", "teams:read"),
		},
		{
			Name:        "Vendas",
			Description: "Vendedor da loja: atende o fluxo de vendas e abre oportunidades no CRM",
			Permissions: with("contacts:read", "contacts:write", "tags:read", "templates:read", "teams:read", "sales_opportunities:read", "sales_opportunities:write"),
		},
		{
			Name:        "Logística",
			Description: "Prazo e entrega: busca ou cria contatos em toda a base, dispara templates e liga para o cliente",
			// contacts:read lists the whole organization's (shared) contacts;
			// contacts:write lets the agent register one that does not exist yet.
			Permissions: with("contacts:read", "contacts:write", "tags:read", "templates:read"),
		},
		{
			Name:        "Supervisor de Loja",
			Description: "Acompanha Vendas e Logística da sua loja, intervém nas conversas e fala com o backoffice no chat interno",
			Permissions: with(
				"contacts:read", "contacts:write", "tags:read", "templates:read", "teams:read",
				"conversations:view_team", "chat.assign:write",
				"sales_opportunities:read", "sales_opportunities:write", "sales_opportunities:view_team",
				"analytics:read", "analytics.agents:read", "analytics.agents:view_team",
				"call_logs:read",
			),
		},
		{
			Name:        "Supervisor de SAC",
			Description: "Acesso operacional completo: todas as lojas, chats de vendas e logística e agentes de SAC",
			Permissions: with(
				"contacts:read", "contacts:write", "contacts:export", "tags:read", "templates:read",
				"conversations:view_all", "chat.assign:write",
				"sales_opportunities:read", "sales_opportunities:write", "sales_opportunities:view_all",
				"team_chat:manage",
				"analytics:read", "analytics.agents:read",
				"call_logs:read", "teams:read", "users:read",
			),
		},
	}
}
