package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

type aiToolsEnv struct {
	app                 *handlers.App
	orgA, orgB          *models.Organization
	adminA, adminB      *models.User
	reader, writer, bad *models.User // in org A: read only, write only, no ai_tools permission
}

func newAIToolsEnv(t *testing.T) aiToolsEnv {
	t.Helper()
	app := newTestApp(t)
	schema := json.RawMessage(`{"type":"object","properties":{}}`)
	mk := func(name string, risk aitools.Risk) aitools.ToolSpec {
		return aitools.ToolSpec{Name: name, Description: "d " + name, Parameters: schema, Risk: risk,
			Factory: func(aitools.Scope) ai.Tool { return nil }}
	}
	cat, err := aitools.NewCatalog(mk("get_order", aitools.RiskRead), mk("write_it", aitools.RiskWrite))
	require.NoError(t, err)
	app.AIToolCatalog = cat

	e := aiToolsEnv{app: app}
	e.orgA, e.orgB = testutil.CreateTestOrganization(t, app.DB), testutil.CreateTestOrganization(t, app.DB)
	roleA, roleB := testutil.CreateAdminRole(t, app.DB, e.orgA.ID), testutil.CreateAdminRole(t, app.DB, e.orgB.ID)
	e.adminA = testutil.CreateTestUser(t, app.DB, e.orgA.ID, testutil.WithRoleID(&roleA.ID))
	e.adminB = testutil.CreateTestUser(t, app.DB, e.orgB.ID, testutil.WithRoleID(&roleB.ID))
	r := testutil.CreateTestRoleWithKeys(t, app.DB, e.orgA.ID, "aitr", []string{"ai_tools:read"})
	w := testutil.CreateTestRoleWithKeys(t, app.DB, e.orgA.ID, "aitw", []string{"ai_tools:write"})
	e.reader = testutil.CreateTestUser(t, app.DB, e.orgA.ID, testutil.WithRoleID(&r.ID))
	e.writer = testutil.CreateTestUser(t, app.DB, e.orgA.ID, testutil.WithRoleID(&w.ID))
	agentRole := testutil.CreateAgentRole(t, app.DB, e.orgA.ID)
	e.bad = testutil.CreateTestUser(t, app.DB, e.orgA.ID, testutil.WithRoleID(&agentRole.ID))
	return e
}

func (e aiToolsEnv) do(t *testing.T, h func(*fastglue.Request) error, org uuid.UUID, user uuid.UUID, name string, body any) (int, map[string]any) {
	t.Helper()
	var req *fastglue.Request
	if body != nil {
		req = testutil.NewJSONRequest(t, body)
	} else {
		req = testutil.NewGETRequest(t)
	}
	testutil.SetAuthContext(req, org, user)
	if name != "" {
		testutil.SetPathParam(req, "name", name)
	}
	require.NoError(t, h(req))
	var resp struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	return testutil.GetResponseStatusCode(req), resp.Data
}

func toolStates(data map[string]any) map[string][2]bool { // name -> {enabled, available}
	out := map[string][2]bool{}
	for _, x := range data["tools"].([]any) {
		m := x.(map[string]any)
		out[m["name"].(string)] = [2]bool{m["enabled"].(bool), m["available"].(bool)}
	}
	return out
}

func TestAITools_ListStartsDisabledAndRespectsTheGlobalSwitch(t *testing.T) {
	e := newAIToolsEnv(t)
	status, data := e.do(t, e.app.ListAITools, e.orgA.ID, e.adminA.ID, "", nil)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, false, data["global_enabled"], "off by default")
	assert.Equal(t, map[string][2]bool{"get_order": {false, false}, "write_it": {false, false}}, toolStates(data))

	_, _ = e.do(t, e.app.SetAIToolEnabled, e.orgA.ID, e.adminA.ID, "get_order", map[string]any{"enabled": true})
	_, _ = e.do(t, e.app.SetAIToolEnabled, e.orgA.ID, e.adminA.ID, "write_it", map[string]any{"enabled": true})

	// enabled, but the global switch is off: not available
	_, data = e.do(t, e.app.ListAITools, e.orgA.ID, e.adminA.ID, "", nil)
	assert.Equal(t, map[string][2]bool{"get_order": {true, false}, "write_it": {true, false}}, toolStates(data))

	// global on: the read tool is available, the write tool is still not
	e.app.Config.AITools.Enabled = true
	_, data = e.do(t, e.app.ListAITools, e.orgA.ID, e.adminA.ID, "", nil)
	assert.Equal(t, true, data["global_enabled"])
	assert.Equal(t, map[string][2]bool{"get_order": {true, true}, "write_it": {true, false}}, toolStates(data))
}

func TestAITools_EnableDisableIsAuditedAgainstTheHuman(t *testing.T) {
	e := newAIToolsEnv(t)
	status, data := e.do(t, e.app.SetAIToolEnabled, e.orgA.ID, e.adminA.ID, "get_order", map[string]any{"enabled": true})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, true, data["enabled"])

	var s models.AIToolSetting
	require.NoError(t, e.app.DB.Where("organization_id = ? AND tool_name = ?", e.orgA.ID, "get_order").First(&s).Error)
	assert.True(t, s.Enabled)
	require.NotNil(t, s.UpdatedByID)
	assert.Equal(t, e.adminA.ID, *s.UpdatedByID)

	status, _ = e.do(t, e.app.SetAIToolEnabled, e.orgA.ID, e.adminA.ID, "get_order", map[string]any{"enabled": false})
	require.Equal(t, fasthttp.StatusOK, status)
	require.NoError(t, e.app.DB.Where("organization_id = ? AND tool_name = ?", e.orgA.ID, "get_order").First(&s).Error)
	assert.False(t, s.Enabled)
	var n int64
	require.NoError(t, e.app.DB.Model(&models.AIToolSetting{}).Where("organization_id = ?", e.orgA.ID).Count(&n).Error)
	assert.EqualValues(t, 1, n, "one row per organization and tool")

	// the existing audit log (a human action) gets a created and an updated entry
	var logs []models.AuditLog
	require.Eventually(t, func() bool {
		e.app.DB.Where("organization_id = ? AND resource_type = ? AND resource_id = ?", e.orgA.ID, "ai_tool", s.ID).Order("created_at").Find(&logs)
		return len(logs) == 2
	}, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, models.AuditActionCreated, logs[0].Action)
	assert.Equal(t, models.AuditActionUpdated, logs[1].Action)
	assert.Equal(t, e.adminA.ID, logs[0].UserID)
}

func TestAITools_PermissionsAndValidation(t *testing.T) {
	e := newAIToolsEnv(t)
	on := map[string]any{"enabled": true}

	// no ai_tools permission at all
	status, _ := e.do(t, e.app.ListAITools, e.orgA.ID, e.bad.ID, "", nil)
	assert.Equal(t, fasthttp.StatusForbidden, status)
	status, _ = e.do(t, e.app.SetAIToolEnabled, e.orgA.ID, e.bad.ID, "get_order", on)
	assert.Equal(t, fasthttp.StatusForbidden, status)

	// read-only can list but not change; write-only can change but not list
	status, _ = e.do(t, e.app.ListAITools, e.orgA.ID, e.reader.ID, "", nil)
	assert.Equal(t, fasthttp.StatusOK, status)
	status, _ = e.do(t, e.app.SetAIToolEnabled, e.orgA.ID, e.reader.ID, "get_order", on)
	assert.Equal(t, fasthttp.StatusForbidden, status)
	status, _ = e.do(t, e.app.ListAITools, e.orgA.ID, e.writer.ID, "", nil)
	assert.Equal(t, fasthttp.StatusForbidden, status)
	status, _ = e.do(t, e.app.SetAIToolEnabled, e.orgA.ID, e.writer.ID, "get_order", on)
	assert.Equal(t, fasthttp.StatusOK, status)

	// a tool that is not in the catalog cannot be enabled
	status, _ = e.do(t, e.app.SetAIToolEnabled, e.orgA.ID, e.adminA.ID, "not_in_catalog", on)
	assert.Equal(t, fasthttp.StatusNotFound, status)
	var n int64
	require.NoError(t, e.app.DB.Model(&models.AIToolSetting{}).Where("tool_name = ?", "not_in_catalog").Count(&n).Error)
	assert.Zero(t, n)

	// the body must say what to do
	status, _ = e.do(t, e.app.SetAIToolEnabled, e.orgA.ID, e.adminA.ID, "get_order", map[string]any{})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}

func TestAITools_TheProductionCatalogIsEmpty(t *testing.T) {
	app := newTestApp(t) // no injected catalog
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateAdminRole(t, app.DB, org.ID)
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	e := aiToolsEnv{app: app}

	status, data := e.do(t, app.ListAITools, org.ID, admin.ID, "", nil)
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Empty(t, data["tools"])
	assert.Equal(t, false, data["global_enabled"])
	status, _ = e.do(t, app.SetAIToolEnabled, org.ID, admin.ID, "anything", map[string]any{"enabled": true})
	assert.Equal(t, fasthttp.StatusNotFound, status, "nothing can be enabled: no real tool exists")
}

func TestAITools_OrganizationsAreIsolated(t *testing.T) {
	e := newAIToolsEnv(t)
	status, _ := e.do(t, e.app.SetAIToolEnabled, e.orgA.ID, e.adminA.ID, "get_order", map[string]any{"enabled": true})
	require.Equal(t, fasthttp.StatusOK, status)

	// B does not see A's opt-in
	_, data := e.do(t, e.app.ListAITools, e.orgB.ID, e.adminB.ID, "", nil)
	assert.Equal(t, [2]bool{false, false}, toolStates(data)["get_order"])

	// B switching the tool does not touch A's row, and vice versa
	_, _ = e.do(t, e.app.SetAIToolEnabled, e.orgB.ID, e.adminB.ID, "get_order", map[string]any{"enabled": false})
	_, data = e.do(t, e.app.ListAITools, e.orgA.ID, e.adminA.ID, "", nil)
	assert.Equal(t, true, toolStates(data)["get_order"][0], "A is still enabled")

	var rows []models.AIToolSetting
	require.NoError(t, e.app.DB.Where("tool_name = ? AND organization_id IN ?", "get_order", []uuid.UUID{e.orgA.ID, e.orgB.ID}).Find(&rows).Error)
	require.Len(t, rows, 2)

	// a user of A authenticated with B's organization id gets nothing of A (the org comes from
	// the authenticated context, never from the request)
	status, _ = e.do(t, e.app.SetAIToolEnabled, e.orgB.ID, e.adminA.ID, "get_order", map[string]any{"enabled": true})
	assert.Equal(t, fasthttp.StatusForbidden, status, "a user without a role in that organization cannot change it")
}
