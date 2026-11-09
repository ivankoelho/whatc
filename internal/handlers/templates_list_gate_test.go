package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
)

// The list of templates is for template managers and for whoever sends messages (the chat picker), not for everybody.
func TestListTemplates_ForManagersAndForWhoSendsMessages(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentKeys := models.SystemRolePermissions()["agent"]
	agent := chatUser(t, app, org.ID, agentKeys)
	chatOnly := chatUser(t, app, org.ID, []string{"chat:read", "chat:write"})
	readOnlyChat := chatUser(t, app, org.ID, []string{"chat:read", "contacts:read"})
	manager := chatUser(t, app, org.ID, []string{"templates:read"})

	for name, u := range map[string]*models.User{"built-in agent role": agent, "chat:write without templates:read": chatOnly, "templates:read": manager} {
		code, _ := chatDo(t, app.ListTemplates, org.ID, u.ID, nil, nil, nil)
		assert.Equal(t, fasthttp.StatusOK, code, name)
	}
	code, _ := chatDo(t, app.ListTemplates, org.ID, readOnlyChat.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "read-only chat user")
	// reading the list for the picker does not open the management of templates
	code, _ = chatDo(t, app.CreateTemplate, org.ID, agent.ID, map[string]any{"name": "x"}, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "the agent still cannot create templates")
}
