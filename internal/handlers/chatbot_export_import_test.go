package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// exportEnvelope mirrors the JSON shape returned by ChatbotExport. The handler
// struct is unexported, so the external test package uses a local mirror.
type exportEnvelope struct {
	Version      int              `json:"version"`
	ExportedAt   string           `json:"exported_at"`
	Flow         map[string]any   `json:"flow"`
	KeywordRules []map[string]any `json:"keyword_rules"`
}

type exportResp struct {
	Data exportEnvelope `json:"data"`
}

type importResp struct {
	Data struct {
		ImportedFlowID   string `json:"imported_flow_id"`
		ImportedFlowName string `json:"imported_flow_name"`
		ImportedKeywords int    `json:"imported_keywords"`
	} `json:"data"`
}

// exportFlow runs ChatbotExport for the given flow and returns the parsed envelope.
func exportFlow(t *testing.T, app *handlers.App, orgID, userID, flowID uuid.UUID) exportEnvelope {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{"flow_id": flowID.String()})
	testutil.SetAuthContext(req, orgID, userID)

	require.NoError(t, app.ChatbotExport(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp exportResp
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	return resp.Data
}

func TestApp_ChatbotExport(t *testing.T) {
	t.Parallel()

	t.Run("serializes flow and associated keyword rules", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		perms := getChatbotFlowPermissions(t, app)
		role := testutil.CreateTestRole(t, app.DB, org.ID, "flow-admin", perms)
		user := testutil.CreateTestUser(t, app.DB, org.ID,
			testutil.WithEmail(testutil.UniqueEmail("export-flow")),
			testutil.WithRoleID(&role.ID),
		)

		// createTestChatbotFlow uses trigger keywords {"hello", "start"}.
		flow := createTestChatbotFlow(t, app, org.ID, "Support Flow")
		createTestKeywordRule(t, app, org.ID, "Greeting", []string{"hello", "hi"}) // overlaps -> associated
		createTestKeywordRule(t, app, org.ID, "Farewell", []string{"bye"})         // no overlap -> excluded

		env := exportFlow(t, app, org.ID, user.ID, flow.ID)

		assert.Equal(t, 1, env.Version)
		assert.NotEmpty(t, env.ExportedAt)
		assert.Equal(t, "Support Flow", env.Flow["name"])
		require.Len(t, env.KeywordRules, 1)
		assert.Equal(t, "Greeting", env.KeywordRules[0]["name"])
	})

	t.Run("rejects missing flow", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		perms := getChatbotFlowPermissions(t, app)
		role := testutil.CreateTestRole(t, app.DB, org.ID, "flow-admin", perms)
		user := testutil.CreateTestUser(t, app.DB, org.ID,
			testutil.WithEmail(testutil.UniqueEmail("export-nf")),
			testutil.WithRoleID(&role.ID),
		)

		req := testutil.NewJSONRequest(t, map[string]any{"flow_id": uuid.New().String()})
		testutil.SetAuthContext(req, org.ID, user.ID)

		require.NoError(t, app.ChatbotExport(req))
		assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
	})

	t.Run("does not export a flow from another org", func(t *testing.T) {
		app := newTestApp(t)
		perms := getChatbotFlowPermissions(t, app)

		orgA := testutil.CreateTestOrganization(t, app.DB)
		flowA := createTestChatbotFlow(t, app, orgA.ID, "Org A Flow")

		orgB := testutil.CreateTestOrganization(t, app.DB)
		roleB := testutil.CreateTestRole(t, app.DB, orgB.ID, "flow-admin", perms)
		userB := testutil.CreateTestUser(t, app.DB, orgB.ID,
			testutil.WithEmail(testutil.UniqueEmail("export-crossorg")),
			testutil.WithRoleID(&roleB.ID),
		)

		req := testutil.NewJSONRequest(t, map[string]any{"flow_id": flowA.ID.String()})
		testutil.SetAuthContext(req, orgB.ID, userB.ID)

		require.NoError(t, app.ChatbotExport(req))
		assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
	})
}

func TestApp_ChatbotImport(t *testing.T) {
	t.Parallel()

	t.Run("round-trips a flow and its keyword rules into another org", func(t *testing.T) {
		app := newTestApp(t)
		perms := getChatbotFlowPermissions(t, app)

		// Source org: build and export a flow with an associated keyword rule.
		orgA := testutil.CreateTestOrganization(t, app.DB)
		roleA := testutil.CreateTestRole(t, app.DB, orgA.ID, "flow-admin", perms)
		userA := testutil.CreateTestUser(t, app.DB, orgA.ID,
			testutil.WithEmail(testutil.UniqueEmail("import-src")),
			testutil.WithRoleID(&roleA.ID),
		)
		flowA := createTestChatbotFlow(t, app, orgA.ID, "Onboarding")
		createTestKeywordRule(t, app, orgA.ID, "Greeting", []string{"hello"})
		env := exportFlow(t, app, orgA.ID, userA.ID, flowA.ID)

		// Target org: fresh install with its own WhatsApp account.
		orgB := testutil.CreateTestOrganization(t, app.DB)
		roleB := testutil.CreateTestRole(t, app.DB, orgB.ID, "flow-admin", perms)
		userB := testutil.CreateTestUser(t, app.DB, orgB.ID,
			testutil.WithEmail(testutil.UniqueEmail("import-dst")),
			testutil.WithRoleID(&roleB.ID),
		)
		account := testutil.CreateTestWhatsAppAccount(t, app.DB, orgB.ID)

		req := testutil.NewJSONRequest(t, map[string]any{
			"whatsapp_account": account.Name,
			"version":          env.Version,
			"flow":             env.Flow,
			"keyword_rules":    env.KeywordRules,
		})
		testutil.SetAuthContext(req, orgB.ID, userB.ID)

		require.NoError(t, app.ChatbotImport(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

		var resp importResp
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		assert.Equal(t, "Onboarding", resp.Data.ImportedFlowName)
		assert.Equal(t, 1, resp.Data.ImportedKeywords)

		// The imported flow is a NEW row in org B, scoped to the chosen account.
		newID, err := uuid.Parse(resp.Data.ImportedFlowID)
		require.NoError(t, err)
		assert.NotEqual(t, flowA.ID, newID)

		var imported models.ChatbotFlow
		require.NoError(t, app.DB.First(&imported, "id = ?", newID).Error)
		assert.Equal(t, orgB.ID, imported.OrganizationID)
		assert.Equal(t, account.Name, imported.WhatsAppAccount)

		var ruleCount int64
		app.DB.Model(&models.KeywordRule{}).Where("organization_id = ?", orgB.ID).Count(&ruleCount)
		assert.Equal(t, int64(1), ruleCount)
	})

	t.Run("suffixes the name when it already exists in the org", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		perms := getChatbotFlowPermissions(t, app)
		role := testutil.CreateTestRole(t, app.DB, org.ID, "flow-admin", perms)
		user := testutil.CreateTestUser(t, app.DB, org.ID,
			testutil.WithEmail(testutil.UniqueEmail("import-collide")),
			testutil.WithRoleID(&role.ID),
		)
		account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)

		flow := createTestChatbotFlow(t, app, org.ID, "Duplicate")
		env := exportFlow(t, app, org.ID, user.ID, flow.ID)

		req := testutil.NewJSONRequest(t, map[string]any{
			"whatsapp_account": account.Name,
			"version":          env.Version,
			"flow":             env.Flow,
			"keyword_rules":    env.KeywordRules,
		})
		testutil.SetAuthContext(req, org.ID, user.ID)

		require.NoError(t, app.ChatbotImport(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

		var resp importResp
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		assert.Equal(t, "Duplicate (imported)", resp.Data.ImportedFlowName)
	})

	t.Run("rejects an unknown WhatsApp account", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		perms := getChatbotFlowPermissions(t, app)
		role := testutil.CreateTestRole(t, app.DB, org.ID, "flow-admin", perms)
		user := testutil.CreateTestUser(t, app.DB, org.ID,
			testutil.WithEmail(testutil.UniqueEmail("import-badacct")),
			testutil.WithRoleID(&role.ID),
		)

		req := testutil.NewJSONRequest(t, map[string]any{
			"whatsapp_account": "does-not-exist",
			"version":          1,
			"flow":             map[string]any{"name": "X"},
			"keyword_rules":    []any{},
		})
		testutil.SetAuthContext(req, org.ID, user.ID)

		require.NoError(t, app.ChatbotImport(req))
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	})

	t.Run("rejects an unsupported version", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		perms := getChatbotFlowPermissions(t, app)
		role := testutil.CreateTestRole(t, app.DB, org.ID, "flow-admin", perms)
		user := testutil.CreateTestUser(t, app.DB, org.ID,
			testutil.WithEmail(testutil.UniqueEmail("import-badver")),
			testutil.WithRoleID(&role.ID),
		)
		account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)

		req := testutil.NewJSONRequest(t, map[string]any{
			"whatsapp_account": account.Name,
			"version":          999,
			"flow":             map[string]any{"name": "X"},
			"keyword_rules":    []any{},
		})
		testutil.SetAuthContext(req, org.ID, user.ID)

		require.NoError(t, app.ChatbotImport(req))
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	})
}
