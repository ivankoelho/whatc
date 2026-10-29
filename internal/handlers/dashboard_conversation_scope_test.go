package handlers_test

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DEF-004: the legacy dashboard and every widget over a conversation-bound source (messages, contacts, transfers,
// sessions) must show only what the viewer could open in /chat. These tests read the REAL results of every surface
// (number, table, chart, grouped, time series, by-id, all-widgets, legacy dashboard) and compare them with the
// conversations each user can see. The scopeExpectation is computed twice, independently of the SQL under test:
// by name, written down by hand from the product rules, and by the per-contact function the rest of the system
// already enforces (CanViewConversationForTest). A widget SQL that merely mirrored its own filter would not pass both.

// The nine conversations. SAC-01..03 reproduce the Rodada 3 homologation scene.
const (
	cSAC01   = "SAC01"        // SAC forwarded it to Vendas L1: active transfer to vend1, by sac1
	cSAC02   = "SAC02"        // SAC forwarded it to Logistica L1: active transfer to log1, by sac1
	cSAC03   = "SAC03"        // assigned to the SAC supervisor
	cCarteir = "CARTEIRA"     // carteira of vend1, no active attendance
	cFilaV1  = "FILA_VENDAS1" // team queue of Vendas L1
	cFilaL2  = "FILA_LOJA2"   // team queue of the Loja 2 team
	cGeral   = "FILA_GERAL"   // general queue, no team, no account default
	cHist    = "HISTORICO"    // only a finished (resumed) transfer
	cLivre   = "LIVRE"        // no transfer, no carteira
)

var scopeAllConversations = []string{cSAC01, cSAC02, cSAC03, cCarteir, cFilaV1, cFilaL2, cGeral, cHist, cLivre}

// scopeExpectedVisible is the product rule written by hand, per user (see TestDefinition: SAC keeps read access to what
// it forwarded; view_team extends to the team-mates' attendances and carteiras; the general queue is view_all only).
var scopeExpectedVisible = map[string][]string{
	"manager":   scopeAllConversations,
	"sac1":      {cSAC01, cSAC02},
	"supSAC":    {cSAC03},
	"vend1":     {cSAC01, cCarteir, cFilaV1},
	"log1":      {cSAC02},
	"supLoja1":  {cSAC01, cSAC02, cCarteir, cFilaV1},
	"supLoja2":  {cFilaL2},
	"agente":    {},
	"juridico":  {},
	"semPerfil": {},
}

// scopeTransferRows is how many agent_transfers rows each conversation has (SAC01 also has a finished earlier one).

type scopeWorld struct {
	app      *handlers.App
	org      *models.Organization
	users    map[string]*models.User
	contacts map[string]*models.Contact
	msg      map[string]*models.Message
	sess     map[string]*models.ChatbotSession
	xfer     map[string][]*models.AgentTransfer
	from, to string
}

func newScopeWorld(t *testing.T) *scopeWorld {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	w := &scopeWorld{
		app: app, org: org,
		users: map[string]*models.User{}, contacts: map[string]*models.Contact{},
		msg: map[string]*models.Message{}, sess: map[string]*models.ChatbotSession{}, xfer: map[string][]*models.AgentTransfer{},
		from: time.Now().AddDate(0, 0, -1).Format("2006-01-02"), to: time.Now().AddDate(0, 0, 1).Format("2006-01-02"),
	}

	mkUser := func(name string, keys ...string) {
		role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, name+uuid.NewString()[:6], keys)
		w.users[name] = testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	}
	mkTeam := func(name string, members ...string) *models.Team {
		tm := &models.Team{OrganizationID: org.ID, Name: name + uuid.NewString()[:6], IsActive: true}
		require.NoError(t, app.DB.Create(tm).Error)
		for _, m := range members {
			require.NoError(t, app.DB.Create(&models.TeamMember{TeamID: tm.ID, UserID: w.users[m].ID}).Error)
		}
		return tm
	}
	base := []string{"chat:read", "contacts:read", "analytics:read"}
	mkUser("manager", append([]string{"conversations:view_all"}, base...)...)
	mkUser("sac1", base...)
	mkUser("supSAC", append([]string{"conversations:view_team"}, base...)...)
	mkUser("vend1", base...)
	mkUser("log1", base...)
	mkUser("supLoja1", append([]string{"conversations:view_team"}, base...)...)
	mkUser("supLoja2", append([]string{"conversations:view_team"}, base...)...)
	mkUser("agente", base...)
	mkUser("juridico", base...)
	mkUser("semPerfil") // a role with no permission at all

	teamSAC := mkTeam("SAC", "sac1", "supSAC")
	_ = teamSAC
	teamV1 := mkTeam("VendasL1", "vend1", "supLoja1")
	teamL1 := mkTeam("LogL1", "log1", "supLoja1")
	teamL2 := mkTeam("Loja2", "supLoja2")
	_ = teamL1

	now := time.Now()
	for i, name := range scopeAllConversations {
		c := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(&models.Contact{}).Where("id = ?", c.ID).Update("last_message_at", now).Error)
		w.contacts[name] = c
		at := now.Add(-time.Duration(i+1) * time.Minute)
		w.msg[name] = createTestMessage(t, app, org.ID, c.ID, models.DirectionIncoming, at)
		w.sess[name] = createTestChatbotSession(t, app, org.ID, c.ID, at)
	}
	xfer := func(name string, status models.TransferStatus, agent, team, by *uuid.UUID) {
		tr := &models.AgentTransfer{
			BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: w.contacts[name].ID,
			PhoneNumber: "+550000", Status: status, Source: models.TransferSourceManual,
			AgentID: agent, TeamID: team, TransferredByUserID: by, TransferredAt: now.Add(-time.Hour),
		}
		require.NoError(t, app.DB.Create(tr).Error)
		w.xfer[name] = append(w.xfer[name], tr)
	}
	id := func(u string) *uuid.UUID { return &w.users[u].ID }
	xfer(cSAC01, models.TransferStatusResumed, id("vend1"), nil, id("sac1")) // an earlier, finished attendance
	xfer(cSAC01, models.TransferStatusActive, id("vend1"), nil, id("sac1"))
	xfer(cSAC02, models.TransferStatusActive, id("log1"), nil, id("sac1"))
	xfer(cSAC03, models.TransferStatusActive, id("supSAC"), nil, nil)
	xfer(cFilaV1, models.TransferStatusActive, nil, &teamV1.ID, nil)
	xfer(cFilaL2, models.TransferStatusActive, nil, &teamL2.ID, nil)
	xfer(cGeral, models.TransferStatusActive, nil, nil, nil)
	xfer(cHist, models.TransferStatusResumed, id("vend1"), nil, id("sac1"))
	require.NoError(t, app.DB.Model(&models.Contact{}).Where("id = ?", w.contacts[cCarteir].ID).
		Update("assigned_user_id", w.users["vend1"].ID).Error)
	for name := range w.contacts { // reload: the oracle reads the contact row
		require.NoError(t, app.DB.First(w.contacts[name], "id = ?", w.contacts[name].ID).Error)
	}

	// Another organization with one of everything: it must never show up, for anybody.
	other := testutil.CreateTestOrganization(t, app.DB)
	oc := testutil.CreateTestContact(t, app.DB, other.ID)
	require.NoError(t, app.DB.Model(&models.Contact{}).Where("id = ?", oc.ID).Update("last_message_at", now).Error)
	createTestMessage(t, app, other.ID, oc.ID, models.DirectionIncoming, now)
	createTestChatbotSession(t, app, other.ID, oc.ID, now)
	require.NoError(t, app.DB.Create(&models.AgentTransfer{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: other.ID, ContactID: oc.ID,
		PhoneNumber: "+550001", Status: models.TransferStatusActive, Source: models.TransferSourceManual, TransferredAt: now,
	}).Error)
	return w
}

// byFunction is the visible set according to the per-contact rule every other endpoint enforces.
func (w *scopeWorld) byFunction(user string) []string {
	var out []string
	for _, name := range scopeAllConversations {
		if w.app.CanViewConversationForTest(w.users[user].ID, w.org.ID, w.contacts[name]) {
			out = append(out, name)
		}
	}
	return out
}

func scopeSorted(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// scopeExpectation is what one user should see, derived from a visible set of conversation names.
type scopeExpectation struct {
	names                              []string
	messages, contacts, sessions, xfer int
	ids                                map[string]map[string]bool // data source -> row ids
}

func (w *scopeWorld) expect(names []string) scopeExpectation {
	e := scopeExpectation{names: names, ids: map[string]map[string]bool{"messages": {}, "contacts": {}, "sessions": {}, "transfers": {}}}
	for _, n := range names {
		e.messages++
		e.contacts++
		e.sessions++
		e.ids["messages"][w.msg[n].ID.String()] = true
		e.ids["contacts"][w.contacts[n].ID.String()] = true
		e.ids["sessions"][w.sess[n].ID.String()] = true
		for _, tr := range w.xfer[n] {
			e.xfer++
			e.ids["transfers"][tr.ID.String()] = true
		}
	}
	return e
}

func (e scopeExpectation) count(source string) float64 {
	switch source {
	case "messages":
		return float64(e.messages)
	case "contacts":
		return float64(e.contacts)
	case "sessions":
		return float64(e.sessions)
	default:
		return float64(e.xfer)
	}
}

// scopeGroupBy is a group-by column each source supports in a real widget.
var scopeGroupBy = map[string]string{"messages": "direction", "contacts": "region", "sessions": "status", "transfers": "status"}

func (w *scopeWorld) widget(source, display, chart, group string) models.Widget {
	return models.Widget{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID, Name: source + display + chart + group,
		DataSource: source, Metric: "count", DisplayType: display, ChartType: chart, GroupByField: group,
	}
}

func scopeSumPoints(p []handlers.DataPoint) (s float64) {
	for _, x := range p {
		s += x.Value
	}
	return
}

func scopeSumChart(p []handlers.ChartPoint) (s float64) {
	for _, x := range p {
		s += x.Value
	}
	return
}

func scopeSumSeries(g *handlers.GroupedSeriesData) (s float64) {
	if g == nil {
		return 0
	}
	for _, d := range g.Datasets {
		for _, v := range d.Data {
			s += v
		}
	}
	return
}

func scopeRowIDs(rows []handlers.TableRow) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		out[r.ID] = true
	}
	return out
}

// assertEveryFormat runs one user through every widget format and every source and compares with the scopeExpectation.
func (w *scopeWorld) assertEveryFormat(t *testing.T, user string, e scopeExpectation) {
	t.Helper()
	uid := w.users[user].ID
	run := func(wd models.Widget) handlers.WidgetDataResponse {
		t.Helper()
		resp, err := w.app.ExecuteWidgetQueryForTest(w.org.ID, uid, wd, w.from, w.to)
		require.NoError(t, err)
		return resp
	}
	for _, src := range []string{"messages", "contacts", "sessions", "transfers"} {
		want := e.count(src)
		ctx := user + "/" + src

		num := run(w.widget(src, "number", "", ""))
		assert.Equal(t, want, num.Value, ctx+" number")

		tbl := run(w.widget(src, "table", "", ""))
		assert.Equal(t, e.ids[src], scopeRowIDs(tbl.TableRows), ctx+" table rows")

		cht := run(w.widget(src, "chart", "bar", ""))
		assert.Equal(t, want, scopeSumChart(cht.ChartData), ctx+" chart (daily)")

		grp := run(w.widget(src, "chart", "bar", scopeGroupBy[src]))
		assert.Equal(t, want, scopeSumPoints(grp.DataPoints), ctx+" grouped (bar)")

		pie := run(w.widget(src, "chart", "pie", scopeGroupBy[src]))
		assert.Equal(t, want, scopeSumPoints(pie.DataPoints), ctx+" grouped (pie)")

		gtbl := run(w.widget(src, "table", "", scopeGroupBy[src]))
		assert.Equal(t, want, scopeSumPoints(gtbl.DataPoints), ctx+" grouped table")

		if src != "contacts" { // contacts only group by region (a derived value): there is no line series for it
			ts := run(w.widget(src, "chart", "line", scopeGroupBy[src]))
			assert.Equal(t, want, scopeSumSeries(ts.GroupedSeries), ctx+" grouped time series")
		}
	}
}

type scopeDashboardOut struct {
	Data struct {
		Stats          handlers.DashboardStats          `json:"stats"`
		RecentMessages []handlers.RecentMessageResponse `json:"recent_messages"`
	} `json:"data"`
}

func (w *scopeWorld) dashboard(t *testing.T, user string) scopeDashboardOut {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, w.org.ID, w.users[user].ID)
	testutil.SetQueryParam(req, "from", w.from)
	testutil.SetQueryParam(req, "to", w.to)
	require.NoError(t, w.app.GetDashboardStats(req))
	require.Equal(t, 200, testutil.GetResponseStatusCode(req))
	var out scopeDashboardOut
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &out))
	return out
}

// The hand-written table and the per-contact function must agree before either is used as a reference.
func TestDashboardScope_FixtureMatchesTheVisibilityFunction(t *testing.T) {
	w := newScopeWorld(t)
	for user, want := range scopeExpectedVisible {
		assert.Equal(t, scopeSorted(want), scopeSorted(w.byFunction(user)), "visible conversations of "+user)
	}
}

func TestDashboardScope_EveryWidgetFormat(t *testing.T) {
	w := newScopeWorld(t)
	for user, names := range scopeExpectedVisible {
		t.Run(user, func(t *testing.T) {
			w.assertEveryFormat(t, user, w.expect(names))
		})
	}
}

func TestDashboardScope_LegacyDashboard(t *testing.T) {
	w := newScopeWorld(t)
	for user, names := range scopeExpectedVisible {
		t.Run(user, func(t *testing.T) {
			e := w.expect(names)
			got := w.dashboard(t, user)
			assert.EqualValues(t, e.messages, got.Data.Stats.TotalMessages, "total messages")
			assert.EqualValues(t, e.contacts, got.Data.Stats.TotalContacts, "total contacts")
			assert.EqualValues(t, e.sessions, got.Data.Stats.ChatbotSessions, "chatbot sessions")

			// recent_messages: the five newest messages among the visible conversations, in order.
			var visible []*models.Message
			for _, n := range names {
				visible = append(visible, w.msg[n])
			}
			sort.Slice(visible, func(i, j int) bool { return visible[i].CreatedAt.After(visible[j].CreatedAt) })
			if len(visible) > 5 {
				visible = visible[:5]
			}
			var wantIDs, gotIDs []string
			for _, m := range visible {
				wantIDs = append(wantIDs, m.ID.String())
			}
			for _, m := range got.Data.RecentMessages {
				gotIDs = append(gotIDs, m.ID)
			}
			assert.Equal(t, wantIDs, gotIDs, "recent_messages")
		})
	}
}

// Rodada 3, DEF-004 on screen: the Loja 2 supervisor saw SAC-01 and SAC-02 (Loja 1) on the Painel.
func TestDashboardScope_SupervisorLoja2DoesNotSeeLoja1Conversations(t *testing.T) {
	w := newScopeWorld(t)
	hidden := map[string]bool{w.msg[cSAC01].ID.String(): true, w.msg[cSAC02].ID.String(): true, w.msg[cSAC03].ID.String(): true}

	for _, m := range w.dashboard(t, "supLoja2").Data.RecentMessages {
		assert.False(t, hidden[m.ID], "recent_messages leaked a conversation of another store")
	}
	tbl, err := w.app.ExecuteWidgetQueryForTest(w.org.ID, w.users["supLoja2"].ID, w.widget("messages", "table", "", ""), w.from, w.to)
	require.NoError(t, err)
	got := scopeRowIDs(tbl.TableRows)
	assert.Equal(t, map[string]bool{w.msg[cFilaL2].ID.String(): true}, got)
	for id := range hidden {
		assert.False(t, got[id])
	}
	for _, src := range []string{"sessions", "transfers", "contacts"} {
		r, err := w.app.ExecuteWidgetQueryForTest(w.org.ID, w.users["supLoja2"].ID, w.widget(src, "table", "", ""), w.from, w.to)
		require.NoError(t, err)
		assert.Len(t, r.TableRows, 1, src+": only the Loja 2 queue")
	}
}

// Who forwarded keeps reading (never interacting): the SAC sees what it sent on to Vendas and Logistica.
func TestDashboardScope_WhoForwardedKeepsSeeingIt(t *testing.T) {
	w := newScopeWorld(t)
	got := w.dashboard(t, "sac1")
	assert.EqualValues(t, 2, got.Data.Stats.TotalMessages)
	tbl, err := w.app.ExecuteWidgetQueryForTest(w.org.ID, w.users["sac1"].ID, w.widget("transfers", "table", "", ""), w.from, w.to)
	require.NoError(t, err)
	// SAC01 has two rows (the finished one and the active one), SAC02 one: all hang off conversations sac1 may read.
	assert.Len(t, tbl.TableRows, 3)
}

// Transfer history follows the conversation, not the person it was once handed to: HISTORICO has only a finished
// transfer to vend1 and nobody but view_all may open that conversation today.
func TestDashboardScope_FinishedTransferDoesNotOpenTheConversation(t *testing.T) {
	w := newScopeWorld(t)
	histID := w.xfer[cHist][0].ID.String()
	for _, user := range []string{"vend1", "sac1", "supLoja1"} {
		tbl, err := w.app.ExecuteWidgetQueryForTest(w.org.ID, w.users[user].ID, w.widget("transfers", "table", "", ""), w.from, w.to)
		require.NoError(t, err)
		assert.False(t, scopeRowIDs(tbl.TableRows)[histID], user+" must not see the finished transfer of a conversation it cannot open")
	}
	tbl, err := w.app.ExecuteWidgetQueryForTest(w.org.ID, w.users["manager"].ID, w.widget("transfers", "table", "", ""), w.from, w.to)
	require.NoError(t, err)
	assert.True(t, scopeRowIDs(tbl.TableRows)[histID], "view_all keeps the organization view")
}

// A request without a viewer is never an organization-wide read.
func TestDashboardScope_NoViewerSeesNothing(t *testing.T) {
	w := newScopeWorld(t)
	for _, src := range []string{"messages", "contacts", "sessions", "transfers"} {
		n, err := w.app.ExecuteWidgetQueryForTest(w.org.ID, uuid.Nil, w.widget(src, "number", "", ""), w.from, w.to)
		require.NoError(t, err)
		assert.Zero(t, n.Value, src)
		tb, err := w.app.ExecuteWidgetQueryForTest(w.org.ID, uuid.Nil, w.widget(src, "table", "", ""), w.from, w.to)
		require.NoError(t, err)
		assert.Empty(t, tb.TableRows, src)
	}
}

// A view_all user of another organization reads that organization only; ours never leaks across.
func TestDashboardScope_OrganizationIsolation(t *testing.T) {
	w := newScopeWorld(t)
	assert.EqualValues(t, 9, w.dashboard(t, "manager").Data.Stats.TotalMessages, "the other organization's message is not counted")

	orgB := testutil.CreateTestOrganization(t, w.app.DB)
	role := testutil.CreateTestRoleWithKeys(t, w.app.DB, orgB.ID, "mgrB", []string{"conversations:view_all", "analytics:read"})
	mgrB := testutil.CreateTestUser(t, w.app.DB, orgB.ID, testutil.WithRoleID(&role.ID))
	cB := testutil.CreateTestContact(t, w.app.DB, orgB.ID)
	createTestMessage(t, w.app, orgB.ID, cB.ID, models.DirectionIncoming, time.Now())

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgB.ID, mgrB.ID)
	testutil.SetQueryParam(req, "from", w.from)
	testutil.SetQueryParam(req, "to", w.to)
	require.NoError(t, w.app.GetDashboardStats(req))
	var out scopeDashboardOut
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &out))
	assert.EqualValues(t, 1, out.Data.Stats.TotalMessages)
	require.Len(t, out.Data.RecentMessages, 1)
}

// By id and "all widgets" are the endpoints the screen uses; own and shared custom widgets must obey the same scope.
func TestDashboardScope_ByIDAndAllWidgetsEndpoints(t *testing.T) {
	w := newScopeWorld(t)
	owner := w.users["manager"].ID
	var shared []models.Widget
	for _, src := range []string{"messages", "contacts", "sessions", "transfers"} {
		for _, display := range []string{"number", "table"} {
			wd := w.widget(src, display, "", "")
			wd.UserID, wd.IsShared = &owner, true
			require.NoError(t, w.app.DB.Create(&wd).Error)
			shared = append(shared, wd)
		}
	}

	for user, names := range scopeExpectedVisible {
		t.Run(user, func(t *testing.T) {
			e := w.expect(names)
			uid := w.users[user].ID

			// An own, unshared custom widget of this very user, over the messages source.
			own := w.widget("messages", "table", "", "")
			own.UserID = &uid
			require.NoError(t, w.app.DB.Create(&own).Error)

			check := func(wd models.Widget, resp handlers.WidgetDataResponse, via string) {
				if wd.DisplayType == "number" {
					assert.Equal(t, e.count(wd.DataSource), resp.Value, via+" "+wd.DataSource+" number")
				} else {
					assert.Equal(t, e.ids[wd.DataSource], scopeRowIDs(resp.TableRows), via+" "+wd.DataSource+" table")
				}
			}

			for _, wd := range append(append([]models.Widget{}, shared...), own) {
				req := testutil.NewGETRequest(t)
				testutil.SetAuthContext(req, w.org.ID, uid)
				testutil.SetPathParam(req, "id", wd.ID.String())
				testutil.SetQueryParam(req, "from", w.from)
				testutil.SetQueryParam(req, "to", w.to)
				require.NoError(t, w.app.GetWidgetData(req))
				require.Equal(t, 200, testutil.GetResponseStatusCode(req))
				var resp handlers.WidgetDataResponse
				testutil.ParseEnvelopeResponse(t, req, &resp)
				check(wd, resp, "by id")
			}

			req := testutil.NewGETRequest(t)
			testutil.SetAuthContext(req, w.org.ID, uid)
			testutil.SetQueryParam(req, "from", w.from)
			testutil.SetQueryParam(req, "to", w.to)
			require.NoError(t, w.app.GetAllWidgetsData(req))
			var all struct {
				Data map[string]handlers.WidgetDataResponse `json:"data"`
			}
			testutil.ParseEnvelopeResponse(t, req, &all)
			for _, wd := range append(append([]models.Widget{}, shared...), own) {
				resp, ok := all.Data[wd.ID.String()]
				require.True(t, ok, "all-widgets response lacks %s", wd.Name)
				check(wd, resp, "all widgets")
			}
		})
	}
}
