package handlers_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// Cross-department operational homologation (Stage 7). Every user here carries EXACTLY the permissions of the
// shipped team profile (models.TeamProfiles(): SAC, Vendas, Logística, Supervisor de Loja, Supervisor de SAC):
// nothing is added to make a scenario pass. A case is "forwarded" to another department the way the product does
// it: the requester opens it with the receiving team. Each scenario is a lifecycle; each step is a subtest, so the
// report reads APROVADO (PASS), REPROVADO (FAIL) or NÃO EXECUTADO (SKIP) per step.

type xdWorld struct {
	*chatWorld
	dept  map[string]*models.Department
	team  map[string]*models.Team
	u     map[string]*models.User // by profile key
	apoio map[string]*models.User // a colleague of the same profile and department, NOT on the case team
	// outsiders
	vendasOutra, logOutra2, foreign *models.User
}

func profilePerms(t *testing.T, name string) []string {
	t.Helper()
	for _, p := range models.TeamProfiles() {
		if p.Name == name {
			return p.Permissions
		}
	}
	t.Fatalf("no team profile %q", name)
	return nil
}

func newXDWorld(t *testing.T) *xdWorld {
	t.Helper()
	nw := newNotifWorld(t)
	cw := &chatWorld{notifWorld: nw}
	cw.app.Config.Storage.LocalPath = t.TempDir()
	w := &xdWorld{chatWorld: cw, dept: map[string]*models.Department{}, team: map[string]*models.Team{}, u: map[string]*models.User{}, apoio: map[string]*models.User{}}
	org := w.org.ID
	db := w.app.DB

	for _, n := range []string{"SAC", "Vendas", "Logística", "Supervisão"} {
		d := &models.Department{OrganizationID: org, Name: n + " " + uuid.NewString()[:4], Active: true}
		require.NoError(t, db.Create(d).Error)
		w.dept[n] = d
	}
	mkTeam := func(name string, unit *models.Unit) *models.Team {
		tm := &models.Team{OrganizationID: org, Name: name + " " + uuid.NewString()[:4], IsActive: true}
		if unit != nil {
			tm.UnitID = &unit.ID
		}
		require.NoError(t, db.Create(tm).Error)
		return tm
	}
	w.team["SAC"], w.team["Log"] = w.teamSAC, w.teamPorto
	w.team["Vendas"] = mkTeam("Vendas Porto", w.unitPorto)
	w.team["SupLoja"] = mkTeam("Supervisão Porto", w.unitPorto)
	w.team["SupSAC"] = mkTeam("Supervisão SAC", nil)
	w.team["VendasOutra"] = mkTeam("Vendas Outra", w.unitOutra)

	profileUser := func(profile, dept, who string) *models.User {
		perms := profilePerms(t, profile)
		if profile == "Vendas" {
			// The shipped Vendas profile has no protocol access (it works in the CRM). These scenarios are about
			// a seller an admin expressly authorized in protocols, so the two permissions are granted on top.
			perms = append(append([]string{}, perms...), "occurrences:read", "occurrences:write")
		}
		role := testutil.CreateTestRoleWithKeys(t, db, org, profile+"-"+who+"-"+uuid.NewString()[:5], perms)
		u := testutil.CreateTestUser(t, db, org, testutil.WithRoleID(&role.ID))
		name := fmt.Sprintf("%s %s %s", profile, who, uuid.NewString()[:4])
		require.NoError(t, db.Model(u).Updates(map[string]any{"full_name": name, "department_id": w.dept[dept].ID}).Error)
		u.FullName = name
		return u
	}
	join := func(u *models.User, teams ...string) {
		for _, k := range teams {
			require.NoError(t, db.Create(&models.TeamMember{TeamID: w.team[k].ID, UserID: u.ID}).Error)
		}
	}
	w.u["SAC"] = profileUser("SAC", "SAC", "titular")
	w.u["Vendas"] = profileUser("Vendas", "Vendas", "titular")
	w.u["Log"] = profileUser("Logística", "Logística", "titular")
	w.u["SupLoja"] = profileUser("Supervisor de Loja", "Supervisão", "titular")
	w.u["SupSAC"] = profileUser("Supervisor de SAC", "Supervisão", "titular")
	join(w.u["SAC"], "SAC")
	join(w.u["Vendas"], "Vendas")
	join(w.u["Log"], "Log")
	join(w.u["SupLoja"], "Vendas", "Log", "SupLoja")
	join(w.u["SupSAC"], "SupSAC")
	w.apoio["SAC"] = profileUser("SAC", "SAC", "apoio")
	w.apoio["Vendas"] = profileUser("Vendas", "Vendas", "apoio")
	w.apoio["Log"] = profileUser("Logística", "Logística", "apoio")
	w.apoio["SupLoja"] = profileUser("Supervisor de Loja", "Supervisão", "apoio")
	w.apoio["SupSAC"] = profileUser("Supervisor de SAC", "Supervisão", "apoio")

	w.vendasOutra = profileUser("Vendas", "Vendas", "outra-loja")
	join(w.vendasOutra, "VendasOutra")
	w.logOutra2 = profileUser("Logística", "Logística", "outra-loja")
	require.NoError(t, db.Create(&models.TeamMember{TeamID: w.teamOutra.ID, UserID: w.logOutra2.ID}).Error)
	other := newIsoWorld(t)
	role := testutil.CreateTestRoleWithKeys(t, other.app.DB, other.org.ID, "Vendas-foreign-"+uuid.NewString()[:5], profilePerms(t, "Vendas"))
	w.foreign = testutil.CreateTestUser(t, other.app.DB, other.org.ID, testutil.WithRoleID(&role.ID))
	return w
}

func (w *xdWorld) stage(t *testing.T, state string) uuid.UUID {
	t.Helper()
	var s models.OccurrenceStage
	require.NoError(t, w.app.DB.Where("organization_id = ? AND team_id IS NULL AND general_state = ?", w.org.ID, state).Order("position").First(&s).Error)
	return s.ID
}

// open creates the case as `by`, addressed to the team (the product's "forward").
func (w *xdWorld) open(t *testing.T, by *models.User, team *models.Team, title string) uuid.UUID {
	t.Helper()
	contact := testutil.CreateTestContact(t, w.app.DB, w.org.ID)
	w.customerWrote(t, contact.ID)
	code, raw := chatDo(t, w.app.CreateOccurrence, by.OrganizationID, by.ID, map[string]any{
		"contact_id": contact.ID.String(), "title": title, "team_id": team.ID.String(),
		"order_ref": "45872", "priority": "high", "description": "Cliente sem previsão de entrega"}, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	var env struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &env))
	return env.Data.ID
}

func (w *xdWorld) on(occID uuid.UUID) {
	w.occID = occID
	w.chID = uuid.Nil
}

func (w *xdWorld) get(t *testing.T, u *models.User, occID uuid.UUID) (int, []byte) {
	return chatDo(t, w.app.GetOccurrence, u.OrganizationID, u.ID, nil, map[string]string{"id": occID.String()}, nil)
}

func (w *xdWorld) note(t *testing.T, u *models.User, occID uuid.UUID, text string) int {
	code, _ := chatDo(t, w.app.CreateOccurrenceEvent, u.OrganizationID, u.ID, map[string]any{"content": text}, map[string]string{"id": occID.String()}, nil)
	return code
}

// whatsapp reads the case's customer conversation. The API answers 200 with restricted=true and NO messages to
// someone who may not read it; that counts as denied (403) here, because no customer data came back.
func (w *xdWorld) whatsapp(t *testing.T, u *models.User, occID uuid.UUID) int {
	code, raw := chatDo(t, w.app.GetOccurrenceWhatsApp, u.OrganizationID, u.ID, nil, map[string]string{"id": occID.String()}, nil)
	if code != fasthttp.StatusOK {
		return code
	}
	var env struct {
		Data struct {
			Restricted bool              `json:"restricted"`
			Messages   []json.RawMessage `json:"messages"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &env)
	if env.Data.Restricted {
		if len(env.Data.Messages) > 0 {
			t.Errorf("restricted but messages were returned")
		}
		return fasthttp.StatusForbidden
	}
	return code
}

func (w *xdWorld) hubFor(t *testing.T, users ...*models.User) map[uuid.UUID]*websocket.Client {
	t.Helper()
	hub := websocket.NewHub(testutil.NopLogger())
	go hub.Run()
	hub.SetOccurrenceAuthorizer(w.app.CanViewOccurrenceByID)
	out := map[uuid.UUID]*websocket.Client{}
	for _, u := range users {
		c := websocket.NewClient(hub, nil, u.ID, u.OrganizationID)
		hub.Register(c)
		out[u.ID] = c
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && hub.GetClientCount() != len(users) {
		time.Sleep(5 * time.Millisecond)
	}
	w.app.WSHub = hub
	return out
}

type xdCase struct {
	name       string
	from, to   string // profile keys
	toTeamKey  string
	fromInTeam bool // the requester already belongs to the receiving team
}

var xdCases = []xdCase{
	{"SAC para Vendas", "SAC", "Vendas", "Vendas", false},
	{"Vendas para SAC", "Vendas", "SAC", "SAC", false},
	{"SAC para Logística", "SAC", "Log", "Log", false},
	{"Logística para SAC", "Log", "SAC", "SAC", false},
	{"Supervisor de Loja para Logística", "SupLoja", "Log", "Log", true},
	{"Logística para Supervisor de Loja", "Log", "SupLoja", "SupLoja", false},
	{"Supervisor de Loja para SAC", "SupLoja", "SAC", "SAC", false},
	{"SAC para Supervisor de Loja", "SAC", "SupLoja", "SupLoja", false},
	{"Supervisor de Loja para Vendas", "SupLoja", "Vendas", "Vendas", true},
	{"Vendas para Supervisor de Loja", "Vendas", "SupLoja", "SupLoja", false},
	{"Supervisor de SAC para SAC", "SupSAC", "SAC", "SAC", false},
	{"SAC para Supervisor de SAC", "SAC", "SupSAC", "SupSAC", false},
}

func ev(t *testing.T, format string, args ...any) { t.Logf("EVID "+format, args...) }

func TestHomologation_CrossDepartmentLifecycle(t *testing.T) {
	for _, tc := range xdCases {
		t.Run(tc.name, func(t *testing.T) {
			w := newXDWorld(t)
			from, to := w.u[tc.from], w.u[tc.to]
			apoio := w.apoio[tc.to]
			team := w.team[tc.toTeamKey]
			var occ uuid.UUID
			var protocol string
			pdf := []byte{}

			t.Run("01 criar e encaminhar", func(t *testing.T) {
				occ = w.open(t, from, team, "Pedido 45872 - "+tc.name)
				w.on(occ)
				var o models.Occurrence
				require.NoError(t, w.app.DB.First(&o, occ).Error)
				protocol = o.ProtocolNumber
				assert.Equal(t, team.ID, *o.TeamID)
				assert.Equal(t, from.ID, o.OpenedByUserID)
				var types []string
				w.app.DB.Model(&models.OccurrenceEvent{}).Where("occurrence_id = ?", occ).Order("created_at").Pluck("type", &types)
				assert.Contains(t, types, string(models.OccurrenceEventForwarded), "the forward is on the timeline")
				ev(t, "perfil_origem=%s perfil_destino=%s org=%s loja=Porto protocolo=%s equipe=%s", tc.from, tc.to, w.org.ID, protocol, team.Name)
			})
			if occ == uuid.Nil {
				t.Fatal("no case: the remaining steps cannot run")
			}

			var sockets map[uuid.UUID]*websocket.Client
			t.Run("02 receber e visualizar", func(t *testing.T) {
				code, raw := w.get(t, to, occ)
				require.Equal(t, fasthttp.StatusOK, code, string(raw))
				assert.True(t, w.listContains(t, to.ID, protocol), "the case is in the receiver's list")
				code, _ = w.get(t, from, occ)
				assert.Equal(t, fasthttp.StatusOK, code, "the requester keeps following it")
				ev(t, "api GET /occurrences/{id} destino=200 origem=200 lista_destino=sim")
			})

			t.Run("03 notificação em tempo real", func(t *testing.T) {
				sockets = w.hubFor(t, from, to, w.vendasOutra, w.foreign)
				require.Equal(t, fasthttp.StatusOK, w.note(t, from, occ, "Posso ajudar com o histórico do cliente."))
				if tc.from != tc.to { // the receiver is notified; the origin gets its own echo; outsiders nothing
					readBroadcast(t, sockets[to.ID], websocket.TypeOccurrenceEventCreated)
				}
				assertNoBroadcast(t, sockets[w.vendasOutra.ID])
				assertNoBroadcast(t, sockets[w.foreign.ID])
				ev(t, "websocket destino=recebeu outra_loja=nada outra_org=nada")
			})

			t.Run("04 só a informação autorizada", func(t *testing.T) {
				// WhatsApp of the customer: follows the person's permissions, not "being on the case"
				wa := w.whatsapp(t, to, occ)
				ev(t, "whatsapp destino(%s)=%d origem(%s)=%d", tc.to, wa, tc.from, w.whatsapp(t, from, occ))
				if !tc.fromInTeam {
					assert.Equal(t, fasthttp.StatusForbidden, w.whatsapp(t, from, occ), "requester that only follows must not read the customer chat")
				}
				// a restricted file is listed but its bytes only go to those who work the case
				f := w.upl2(t, to, occ, "interno.pdf", pdfBytes())
				if !tc.fromInTeam && !(tc.from == "SupSAC") {
					code, _, _ := w.fileCall(t, w.app.DownloadOccurrenceFile, from, occ, f.ID.String(), "")
					assert.Equal(t, fasthttp.StatusForbidden, code, "restricted file for a follower")
				}
			})

			t.Run("05 interação do destino", func(t *testing.T) {
				require.Equal(t, fasthttp.StatusOK, w.note(t, to, occ, "Recebido, vou tratar."))
				code, page := w.history(t, to, occ, "")
				require.Equal(t, fasthttp.StatusOK, code)
				assert.NotEmpty(t, page.Events)
			})

			t.Run("06 origem recebe a atualização", func(t *testing.T) {
				code, page := w.history(t, from, occ, "")
				require.Equal(t, fasthttp.StatusOK, code)
				found := false
				for _, e := range page.Events {
					if strings.Contains(e.Content, "Recebido, vou tratar.") {
						found = true
					}
				}
				assert.True(t, found, "the requester sees the receiver's update")
			})

			t.Run("07 adicionar pessoa de apoio", func(t *testing.T) {
				code, raw := chatDo(t, w.app.AddOccurrenceParticipant, to.OrganizationID, to.ID,
					map[string]any{"user_id": apoio.ID.String(), "role": "support", "department_id": apoio.DepartmentID.String()},
					map[string]string{"id": occ.String()}, nil)
				require.Equal(t, fasthttp.StatusOK, code, string(raw))
				// support opens and comments, nothing more
				code, _ = w.get(t, apoio, occ)
				assert.Equal(t, fasthttp.StatusOK, code)
				assert.Equal(t, fasthttp.StatusOK, w.note(t, apoio, occ, "Apoio: posso consultar o estoque."))
				assert.Equal(t, fasthttp.StatusForbidden, w.whatsapp(t, apoio, occ), "support is not access to the customer's WhatsApp")
				other := w.open(t, from, team, "outro protocolo")
				code, _ = w.get(t, apoio, other)
				if tc.to == "SupSAC" {
					ev(t, "observação: o perfil Supervisor de SAC enxerga todos os protocolos por occurrences:view_all (não é efeito do apoio)")
				} else {
					assert.NotEqual(t, fasthttp.StatusOK, code, "support never opens other protocols")
				}
			})

			t.Run("08 discussão interna do caso", func(t *testing.T) {
				for _, p := range []struct {
					u    *models.User
					text string
				}{{from, "origem: cliente aguarda"}, {to, "destino: em apuração"}, {apoio, "apoio: confirmei o estoque"}} {
					code, raw := chatDo(t, w.app.GetOccurrenceChat, p.u.OrganizationID, p.u.ID, nil, map[string]string{"id": occ.String()}, nil)
					require.Equal(t, fasthttp.StatusOK, code, string(raw))
					ch := decodeChat[occChatState](t, raw).ChannelID
					code, raw = chatDo(t, w.app.SendChatMessage, p.u.OrganizationID, p.u.ID, map[string]any{"body": p.text}, map[string]string{"id": ch.String()}, nil)
					require.Equal(t, fasthttp.StatusOK, code, string(raw))
					w.chID = ch
				}
				code, msgs := w.read(t, to.ID)
				require.Equal(t, fasthttp.StatusOK, code)
				assert.Len(t, msgs, 3)
				assert.Equal(t, fasthttp.StatusNotFound, w.say(t, w.vendasOutra.ID, "intruso"), "someone out of the case does not even see the channel")
				assert.Equal(t, fasthttp.StatusOK, w.whatsappDeniedForDiscussionOnly(t, apoio, occ), "taking part in the discussion grants no WhatsApp")
			})

			t.Run("09 mesma discussão no Chat Interno central", func(t *testing.T) {
				for _, u := range []*models.User{from, to, apoio} {
					c := w.caseChannel(w.channelsOf(t, u.OrganizationID, u.ID))
					require.NotNil(t, c, "%s sees the case channel in the central chat", u.FullName)
					assert.Equal(t, w.chID, c.ID, "the very same channel")
				}
				assert.Nil(t, w.caseChannel(w.channelsOf(t, w.vendasOutra.OrganizationID, w.vendasOutra.ID)))
				assert.Nil(t, w.caseChannel(w.channelsOf(t, w.foreign.OrganizationID, w.foreign.ID)))
			})

			var pdfFile, pngFile string
			t.Run("10 anexar PDF e imagem", func(t *testing.T) {
				f1 := w.upl2(t, to, occ, "laudo.pdf", pdfBytes())
				f2 := w.upl2(t, to, occ, "foto.png", pngBytes(4, 4), "restricted", "false")
				pdfFile, pngFile = f1.ID.String(), f2.ID.String()
				assert.Equal(t, models.ScanClean, f1.ScanStatus)
				assert.Equal(t, models.ScanClean, f2.ScanStatus)
			})

			t.Run("11 visualizar no navegador (inline) e antimalware", func(t *testing.T) {
				code, body, req := w.fileCall(t, w.app.DownloadOccurrenceFile, to, occ, pdfFile, "inline=1")
				require.Equal(t, fasthttp.StatusOK, code)
				pdf = body
				assert.Contains(t, string(req.RequestCtx.Response.Header.Peek("Content-Disposition")), "inline")
				assert.Equal(t, "application/pdf", string(req.RequestCtx.Response.Header.Peek("Content-Type")))
				assert.Equal(t, "nosniff", string(req.RequestCtx.Response.Header.Peek("X-Content-Type-Options")))
				assert.Contains(t, string(req.RequestCtx.Response.Header.Peek("Content-Security-Policy")), "sandbox")
				code, _, req = w.fileCall(t, w.app.DownloadOccurrenceFile, apoio, occ, pngFile, "inline=1")
				assert.Equal(t, fasthttp.StatusOK, code, "an open (unrestricted) image is viewable by the support person")
				assert.Equal(t, "image/png", string(req.RequestCtx.Response.Header.Peek("Content-Type")))
				// an infected file never becomes viewable by anyone
				code, raw := w.fileUpload(t, to, occ, "virus.pdf", eicarPDF())
				if code == fasthttp.StatusOK {
					bad := uploaded(t, raw).File
					assert.NotEqual(t, models.ScanClean, bad.ScanStatus)
					assert.NotEqual(t, fasthttp.StatusOK, w.dl(t, to, bad.ID, "inline=1"))
				}
			})

			t.Run("12 etapa, prioridade e responsável", func(t *testing.T) {
				code, raw := chatDo(t, w.app.ClaimOccurrence, to.OrganizationID, to.ID, map[string]any{}, map[string]string{"id": occ.String()}, nil)
				if code != fasthttp.StatusOK && tc.fromInTeam {
					// a case opened by someone who is already in the receiving team stays with that person; the
					// receiver gets it by redistribution (the requester here is a supervisor)
					ev(t, "observação: caso aberto por quem já está na equipe destino fica com quem abriu; o destino recebe por redistribuição")
					code, raw = chatDo(t, w.app.ReassignOccurrence, from.OrganizationID, from.ID, map[string]any{"assigned_user_id": to.ID.String()}, map[string]string{"id": occ.String()}, nil)
				}
				require.Equal(t, fasthttp.StatusOK, code, string(raw))
				code, raw = chatDo(t, w.app.ChangeOccurrenceStage, to.OrganizationID, to.ID, map[string]any{"stage_id": w.stage(t, "in_progress").String()}, map[string]string{"id": occ.String()}, nil)
				require.Equal(t, fasthttp.StatusOK, code, string(raw))
				code, raw = chatDo(t, w.app.UpdateOccurrence, to.OrganizationID, to.ID, map[string]any{"title": "Pedido 45872 - " + tc.name, "priority": "urgent"}, map[string]string{"id": occ.String()}, nil)
				require.Equal(t, fasthttp.StatusOK, code, string(raw))
				var o models.Occurrence
				require.NoError(t, w.app.DB.First(&o, occ).Error)
				assert.Equal(t, models.OccurrencePriorityUrgent, o.Priority)
				require.NotNil(t, o.AssignedUserID)
				assert.Equal(t, to.ID, *o.AssignedUserID)
				// a stage change by a supervisor does not move the owner
				if tc.to != "SupSAC" {
					code, _ = chatDo(t, w.app.ChangeOccurrenceStage, w.u["SupSAC"].OrganizationID, w.u["SupSAC"].ID, map[string]any{"stage_id": w.stage(t, "waiting_customer").String()}, map[string]string{"id": occ.String()}, nil)
					require.Equal(t, fasthttp.StatusOK, code)
					require.NoError(t, w.app.DB.First(&o, occ).Error)
					assert.Equal(t, to.ID, *o.AssignedUserID, "moving the stage does not change who owns the case")
				}
			})

			t.Run("13 devolver e reencaminhar (mesmo protocolo)", func(t *testing.T) {
				do := func(h func(*fastglue.Request) error, body map[string]any) (int, []byte) {
					return chatDo(t, h, to.OrganizationID, to.ID, body, map[string]string{"id": occ.String()}, nil)
				}
				// a return needs a reason
				code, _ := do(w.app.ReturnOccurrence, map[string]any{"reason": " "})
				assert.Equal(t, fasthttp.StatusBadRequest, code)
				// the requester only follows: cannot hand the case over
				if !tc.fromInTeam && tc.from != "SupSAC" && tc.from != "SupLoja" {
					c2, _ := chatDo(t, w.app.ReturnOccurrence, from.OrganizationID, from.ID, map[string]any{"reason": "tentando devolver"}, map[string]string{"id": occ.String()}, nil)
					assert.Equal(t, fasthttp.StatusForbidden, c2, "a follower cannot return the case")
				}
				code, raw := do(w.app.ReturnOccurrence, map[string]any{"reason": "Faltam dados do cliente; devolvo à origem."})
				require.Equal(t, fasthttp.StatusOK, code, string(raw))
				var o models.Occurrence
				require.NoError(t, w.app.DB.First(&o, occ).Error)
				assert.Equal(t, occ, o.ID, "same protocol, no new case")
				assert.Equal(t, protocol, o.ProtocolNumber)
				assert.Nil(t, o.TeamID, "back with the requester")
				// again: nothing to return, no ping-pong
				code, _ = do(w.app.ReturnOccurrence, map[string]any{"reason": "de novo"})
				assert.Contains(t, []int{fasthttp.StatusForbidden, fasthttp.StatusConflict}, code, "after the return the receiver cannot return it again (lost access or nothing to return)")
				// the requester forwards it again
				c3, raw := chatDo(t, w.app.ForwardOccurrence, from.OrganizationID, from.ID, map[string]any{"team_id": team.ID.String(), "reason": "Dados enviados, reencaminho."}, map[string]string{"id": occ.String()}, nil)
				require.Equal(t, fasthttp.StatusOK, c3, string(raw))
				require.NoError(t, w.app.DB.First(&o, occ).Error)
				require.NotNil(t, o.TeamID)
				assert.Equal(t, team.ID, *o.TeamID)
				var moves int64
				w.app.DB.Model(&models.OccurrenceTransfer{}).Where("occurrence_id = ?", occ).Count(&moves)
				assert.EqualValues(t, 3, moves, "open, return, forward: all recorded")
				// history, discussion and files are still there
				_, msgs := w.read(t, to.ID)
				assert.Len(t, msgs, 3)
				code, _, _ = w.fileCall(t, w.app.DownloadOccurrenceFile, to, occ, pdfFile, "")
				assert.Equal(t, fasthttp.StatusOK, code)
				// the receiver takes it again
				code, raw = do(w.app.ClaimOccurrence, map[string]any{})
				require.Equal(t, fasthttp.StatusOK, code, string(raw))
			})

			t.Run("14 encerrar e reabrir", func(t *testing.T) {
				closing := w.stage(t, "resolved")
				code, raw := chatDo(t, w.app.ChangeOccurrenceStage, to.OrganizationID, to.ID, map[string]any{"stage_id": closing.String()}, map[string]string{"id": occ.String()}, nil)
				require.Equal(t, fasthttp.StatusOK, code, string(raw))
				var o models.Occurrence
				require.NoError(t, w.app.DB.First(&o, occ).Error)
				assert.NotNil(t, o.ClosedAt)
				code, _ = w.get(t, from, occ)
				assert.Equal(t, fasthttp.StatusOK, code, "the requester still sees the closed case")
				code, raw = chatDo(t, w.app.ChangeOccurrenceStage, to.OrganizationID, to.ID, map[string]any{"stage_id": w.stage(t, "in_progress").String()}, map[string]string{"id": occ.String()}, nil)
				require.Equal(t, fasthttp.StatusOK, code, string(raw))
				var reopened models.Occurrence // a fresh value: scanning NULL into a populated struct keeps the old time
				require.NoError(t, w.app.DB.First(&reopened, occ).Error)
				assert.Nil(t, reopened.ClosedAt)
				assert.Equal(t, 1, reopened.ReopenedCount)
			})

			t.Run("15 relatório PDF do caso", func(t *testing.T) {
				code, body, _ := w.export(t, to, occ, []string{"full"}, "pdf")
				require.Equal(t, fasthttp.StatusOK, code)
				text := pdfText(t, body)
				flat := strings.Join(strings.Fields(text), " ")
				assert.Contains(t, flat, protocol)
				assert.Contains(t, text, "PARTES ENVOLVIDAS")
				assert.Contains(t, text, "Solicitante")
				assert.Contains(t, flat, from.FullName)
				assert.Contains(t, flat, apoio.FullName, "the support person is listed")
				assert.Contains(t, text, "Pessoa de apoio")
				assert.Contains(t, text, "REGISTRO DE EMISSÃO")
				assert.Regexp(t, regexp.MustCompile(`\d{2}/\d{2}/\d{4}`), text)
				assert.NotRegexp(t, regexp.MustCompile(`(?i)\b(AM|PM)\b`), text)
				// the requester gets a report too, within what that person may read
				code, body, _ = w.export(t, from, occ, []string{"summary", "history", "documents"}, "pdf")
				assert.Equal(t, fasthttp.StatusOK, code)
				assert.NotEmpty(t, body)
				// what they may not: the customer's conversation
				if !tc.fromInTeam {
					code, _, _ = w.export(t, from, occ, []string{"whatsapp"}, "pdf")
					assert.Equal(t, fasthttp.StatusForbidden, code)
				}
			})

			t.Run("16 linha do tempo, responsáveis e apoios", func(t *testing.T) {
				var types []string
				w.app.DB.Model(&models.OccurrenceEvent{}).Where("occurrence_id = ?", occ).Order("created_at, id").Pluck("type", &types)
				for _, want := range []models.OccurrenceEventType{models.OccurrenceEventOpened, models.OccurrenceEventForwarded,
					models.OccurrenceEventParticipantAdded, models.OccurrenceEventStageChange, models.OccurrenceEventClosed, models.OccurrenceEventReopened} {
					assert.Contains(t, types, string(want))
				}
				var o models.Occurrence
				require.NoError(t, w.app.DB.First(&o, occ).Error)
				assert.Equal(t, from.ID, o.OpenedByUserID, "the requester is never lost")
				assert.Equal(t, to.ID, *o.AssignedUserID)
			})

			t.Run("17 datas em pt-BR (DD/MM/AAAA HH:mm, 24 h)", func(t *testing.T) {
				code, body, _ := w.export(t, to, occ, []string{"history"}, "csv")
				require.Equal(t, fasthttp.StatusOK, code)
				assert.Regexp(t, regexp.MustCompile(`\d{2}/\d{2}/\d{4} \d{2}:\d{2}`), string(body))
				assert.NotRegexp(t, regexp.MustCompile(`\d{4}-\d{2}-\d{2}T`), string(body))
				_, raw := w.get(t, to, occ)
				assert.Regexp(t, regexp.MustCompile(`"opened_at":"\d{4}-\d{2}-\d{2}T[^"]*(Z|[+-]\d{2}:\d{2})"`), string(raw), "stored/transported with an explicit zone")
			})
			_ = pdf
			_ = sockets
		})
	}
}

// the discussion alone never opens the customer conversation
func (w *xdWorld) whatsappDeniedForDiscussionOnly(t *testing.T, u *models.User, occ uuid.UUID) int {
	if w.whatsapp(t, u, occ) == fasthttp.StatusForbidden {
		return fasthttp.StatusOK
	}
	return -1
}

func (w *xdWorld) upl2(t *testing.T, u *models.User, occ uuid.UUID, name string, data []byte, fields ...string) handlers.OccurrenceFileResponse {
	t.Helper()
	code, raw := w.fileUpload(t, u, occ, name, data, fields...)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	return uploaded(t, raw).File
}
