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

	without := func(keys []string, drop ...string) []string {
		out := make([]string, 0, len(keys))
		for _, k := range keys {
			keep := true
			for _, d := range drop {
				if k == d {
					keep = false
				}
			}
			if keep {
				out = append(out, k)
			}
		}
		return out
	}

	return []TeamProfile{
		{
			Name:        "SAC",
			Description: "Atendimento ao cliente: recebe a conversa após o fluxo e a transfere para Vendas ou Logística",
			Permissions: with("contacts:read", "tags:read", "templates:read"),
		},
		{
			Name:        "Vendas",
			Description: "Vendedor da loja: atende o fluxo de vendas e abre oportunidades no CRM",
			// Sellers work in the CRM, never in the internal protocols (those are for SAC, Jurídico, store supervisors
			// and the Logística support): so the agent baseline's occurrences:* is taken out.
			Permissions: without(with("contacts:read", "contacts:write", "tags:read", "templates:read", "sales_opportunities:read", "sales_opportunities:write"),
				"occurrences:read", "occurrences:write"),
		},
		{
			Name:        "Logística",
			Description: "Prazo e entrega: busca ou cria contatos em toda a base, dispara templates e liga para o cliente",
			// contacts:read lists the whole organization's (shared) contacts;
			// contacts:write lets the agent register one that does not exist yet.
			Permissions: with("contacts:read", "contacts:write", "tags:read", "templates:read"),
		},
		{
			Name:        "Jurídico",
			Description: "Trabalha os protocolos do jurídico: abre, acompanha e registra casos, consulta documentos e histórico e participa da discussão interna. Não atende conversas de WhatsApp nem usa o CRM comercial",
			// Deliberately NOT built on the agent baseline: no chat (the WhatsApp conversations of the stores), no
			// transfers, calls or sales funnel. Cases are scoped by team membership (occurrence_access.go), so each
			// lawyer sees only the cases of the team(s) they belong to; occurrences:view_all is an explicit extra.
			// contacts:read only finds the customer a case is opened for; it never opens a conversation.
			Permissions: []string{
				"occurrences:read", "occurrences:write",
				"team_chat:read", "team_chat:write",
				"contacts:read",
			},
		},
		{
			Name:        "Supervisor de Loja",
			Description: "Acompanha Vendas e Logística da sua loja, intervém nas conversas e fala com o backoffice no chat interno",
			Permissions: with(
				"contacts:read", "contacts:write", "tags:read", "templates:read",
				"conversations:view_team", "chat.assign:write",
				"sales_opportunities:read", "sales_opportunities:write", "sales_opportunities:view_team",
				"analytics:read", "analytics.agents:read", "analytics.agents:view_team",
				"call_logs:read",
			),
		},
		{
			Name:        "Supervisor de SAC",
			Description: "Supervisiona os chamados de todas as lojas e os agentes de SAC; só lê as conversas das contas e equipes a que pertence (conversations:view_all é concessão explícita)",
			Permissions: with(
				"contacts:read", "contacts:write", "contacts:export", "tags:read", "templates:read",
				"conversations:view_team", "occurrences:view_all", "chat.assign:write",
				"sales_opportunities:read", "sales_opportunities:write", "sales_opportunities:view_all",
				"team_chat:manage",
				"analytics:read", "analytics.agents:read",
				"call_logs:read", "teams:read", "users:read",
			),
		},
	}
}
