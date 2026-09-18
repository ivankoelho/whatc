package handlers

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExecChatButtons_CreateOpportunityFlagOpensOpportunity verifies that
// picking a button configured with create_opportunity: true opens a sales
// funnel entry for the contact (spec §5).
func TestExecChatButtons_CreateOpportunityFlagOpensOpportunity(t *testing.T) {
	app, org, account, contact, session := newGraphTestFixtures(t)

	flow := &models.ChatbotFlow{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		WhatsAppAccount: account.Name, Name: "menu-opportunity", IsEnabled: true,
		Graph: models.JSONB{
			"version": 2, "entry_node": "b1",
			"nodes": []any{
				map[string]any{"id": "b1", "type": "buttons", "label": "menu",
					"config": map[string]any{"body": "O que deseja?",
						"buttons": []any{
							map[string]any{"id": "realizar_pedido", "title": "Realizar pedido", "create_opportunity": true},
							map[string]any{"id": "acompanhar_pedido", "title": "Acompanhar pedido"},
						}}},
				map[string]any{"id": "e1", "type": "end", "label": "done"},
			},
			"edges": []any{
				map[string]any{"from": "b1", "to": "e1", "condition": "button:realizar_pedido"},
				map[string]any{"from": "b1", "to": "e1", "condition": "button:acompanhar_pedido"},
			},
		},
	}
	require.NoError(t, app.DB.Create(flow).Error)

	require.NoError(t, app.runChatGraph(account, contact, session, flow, "start", "", nil))           // park at b1
	require.NoError(t, app.runChatGraph(account, contact, session, flow, "", "realizar_pedido", nil)) // pick flagged button

	var opp models.SalesOpportunity
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&opp).Error)
	assert.Equal(t, models.SalesOpportunityStagePotencial, opp.Stage)
}

// TestExecChatButtons_NoCreateOpportunityFlagDoesNothing verifies that a
// button click with no create_opportunity flag never touches the sales
// funnel.
func TestExecChatButtons_NoCreateOpportunityFlagDoesNothing(t *testing.T) {
	app, org, account, contact, session := newGraphTestFixtures(t)

	flow := &models.ChatbotFlow{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		WhatsAppAccount: account.Name, Name: "menu-no-opportunity", IsEnabled: true,
		Graph: models.JSONB{
			"version": 2, "entry_node": "b1",
			"nodes": []any{
				map[string]any{"id": "b1", "type": "buttons", "label": "menu",
					"config": map[string]any{"body": "O que deseja?",
						"buttons": []any{map[string]any{"id": "acompanhar_pedido", "title": "Acompanhar pedido"}}}},
				map[string]any{"id": "e1", "type": "end", "label": "done"},
			},
			"edges": []any{
				map[string]any{"from": "b1", "to": "e1", "condition": "button:acompanhar_pedido"},
			},
		},
	}
	require.NoError(t, app.DB.Create(flow).Error)

	require.NoError(t, app.runChatGraph(account, contact, session, flow, "start", "", nil))
	require.NoError(t, app.runChatGraph(account, contact, session, flow, "", "acompanhar_pedido", nil))

	var count int64
	require.NoError(t, app.DB.Model(&models.SalesOpportunity{}).Where("contact_id = ?", contact.ID).Count(&count).Error)
	assert.EqualValues(t, 0, count)
}
