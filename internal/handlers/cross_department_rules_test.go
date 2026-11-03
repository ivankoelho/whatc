package handlers_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Stage 7, restrictions: what must NOT work, by interface-level API call, direct URL and by profile.

func TestHomologation_ForbiddenAttempts(t *testing.T) {
	w := newXDWorld(t)
	sac, log := w.u["SAC"], w.u["Log"]
	occ := w.open(t, sac, w.team["Log"], "Pedido 45872 - tentativas proibidas")
	w.on(occ)
	f := w.upl2(t, log, occ, "restrito.pdf", pdfBytes())
	require.NoError(t, w.app.DB.Create(&models.OccurrenceParticipant{OrganizationID: w.org.ID, OccurrenceID: occ, UserID: w.apoio["Log"].ID, Capabilities: []string{"comment"}, Role: "support"}).Error)

	outsiders := map[string]*models.User{
		"Vendas da mesma loja, sem vínculo":     w.u["Vendas"],
		"Vendas de outra loja":                  w.vendasOutra,
		"Logística de outra loja":               w.logOutra2,
		"usuário de outra organização":          w.foreign,
		"SAC colega que não está no caso (apoio sem vínculo)": w.apoio["SAC"],
	}
	for name, u := range outsiders {
		t.Run("sem acesso: "+name, func(t *testing.T) {
			ok := func(code int) bool { return code == fasthttp.StatusForbidden || code == fasthttp.StatusNotFound }
			code, _ := w.get(t, u, occ)
			assert.True(t, ok(code), "GET occurrence: %d", code)
			assert.False(t, w.listedFor(t, u, w.protocolOf(t, occ)), "not even listed")
			c, _, _ := w.fileCall(t, w.app.ListOccurrenceFiles, u, occ, "", "")
			assert.True(t, ok(c), "list files: %d", c)
			c, _, _ = w.fileCall(t, w.app.DownloadOccurrenceFile, u, occ, f.ID.String(), "")
			assert.True(t, ok(c), "direct attachment URL: %d", c)
			c, _, _ = w.fileCall(t, w.app.DownloadOccurrenceFile, u, occ, f.ID.String(), "inline=1")
			assert.True(t, ok(c), "direct attachment URL (inline): %d", c)
			assert.True(t, ok(w.whatsapp(t, u, occ)), "WhatsApp")
			assert.True(t, ok(w.note2(t, u, occ)), "write to the case")
			cc, _ := chatDo(t, w.app.GetOccurrenceChat, u.OrganizationID, u.ID, nil, map[string]string{"id": occ.String()}, nil)
			assert.True(t, ok(cc), "case discussion: %d", cc)
			ec, _, _ := w.export(t, u, occ, []string{"summary"}, "pdf")
			assert.True(t, ok(ec), "export: %d", ec)
			cc, _ = chatDo(t, w.app.AddOccurrenceParticipant, u.OrganizationID, u.ID, map[string]any{"user_id": u.ID.String()}, map[string]string{"id": occ.String()}, nil)
			assert.True(t, ok(cc), "add yourself as a follower: %d", cc)
			cc, _ = chatDo(t, w.app.ListSupportOptions, u.OrganizationID, u.ID, nil, map[string]string{"id": occ.String()}, nil)
			assert.True(t, ok(cc), "support options: %d", cc)
			cc, _ = chatDo(t, w.app.DeleteOccurrence, u.OrganizationID, u.ID, nil, map[string]string{"id": occ.String()}, nil)
			assert.True(t, ok(cc), "delete: %d", cc)
		})
	}

	t.Run("WhatsApp sem chat:read e chat:write, mesmo estando na equipe", func(t *testing.T) {
		var perms []string
		for _, p := range profilePerms(t, "Logística") {
			if !strings.HasPrefix(p, "chat:") {
				perms = append(perms, p)
			}
		}
		role := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "Logistica-sem-chat-"+uuid.NewString()[:5], perms)
		u := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&role.ID))
		require.NoError(t, w.app.DB.Create(&models.TeamMember{TeamID: w.team["Log"].ID, UserID: u.ID}).Error)
		code, _ := w.get(t, u, occ)
		assert.Equal(t, fasthttp.StatusOK, code, "still works the case")
		assert.Equal(t, fasthttp.StatusForbidden, w.whatsapp(t, u, occ))
		c, _ := chatDo(t, w.app.SendOccurrenceWhatsApp, u.OrganizationID, u.ID, map[string]any{"content": "oi"}, map[string]string{"id": occ.String()}, nil)
		assert.Equal(t, fasthttp.StatusForbidden, c)
		ec, _, _ := w.export(t, u, occ, []string{"whatsapp"}, "pdf")
		assert.Equal(t, fasthttp.StatusForbidden, ec, "and cannot export it either")
	})

	t.Run("editar, apagar e exportar sem permissão", func(t *testing.T) {
		role := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "leitura-"+uuid.NewString()[:5], []string{"occurrences:read"})
		u := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&role.ID))
		require.NoError(t, w.app.DB.Create(&models.TeamMember{TeamID: w.team["Log"].ID, UserID: u.ID}).Error)
		c, _ := chatDo(t, w.app.UpdateOccurrence, u.OrganizationID, u.ID, map[string]any{"title": "x"}, map[string]string{"id": occ.String()}, nil)
		assert.Equal(t, fasthttp.StatusForbidden, c, "edit without occurrences:write")
		c, _ = chatDo(t, w.app.ChangeOccurrenceStage, u.OrganizationID, u.ID, map[string]any{"stage_id": w.stage(t, "in_progress").String()}, map[string]string{"id": occ.String()}, nil)
		assert.Equal(t, fasthttp.StatusForbidden, c)
		// nobody in the shipped profiles may delete a case
		for k, p := range w.u {
			c, _ = chatDo(t, w.app.DeleteOccurrence, p.OrganizationID, p.ID, nil, map[string]string{"id": occ.String()}, nil)
			assert.NotEqual(t, fasthttp.StatusOK, c, "%s must not delete a protocol", k)
		}
		var n int64
		w.app.DB.Model(&models.Occurrence{}).Where("id = ?", occ).Count(&n)
		assert.EqualValues(t, 1, n, "the protocol is still there")
	})

	t.Run("apoio de departamento não autorizado", func(t *testing.T) {
		// Logística may pick from its own department, not from Vendas'
		c, raw := chatDo(t, w.app.AddOccurrenceParticipant, log.OrganizationID, log.ID,
			map[string]any{"user_id": w.apoio["Vendas"].ID.String(), "role": "support", "department_id": w.dept["Vendas"].ID.String()},
			map[string]string{"id": occ.String()}, nil)
		assert.Equal(t, fasthttp.StatusForbidden, c, string(raw))
		c, _ = chatDo(t, w.app.ListSupportOptions, log.OrganizationID, log.ID, nil, map[string]string{"id": occ.String()}, map[string]string{"department_id": w.dept["Vendas"].ID.String()})
		assert.Equal(t, fasthttp.StatusForbidden, c)
	})

	t.Run("participar da discussão não dá WhatsApp nem anexo restrito", func(t *testing.T) {
		ap := w.apoio["Log"]
		c, raw := chatDo(t, w.app.GetOccurrenceChat, ap.OrganizationID, ap.ID, nil, map[string]string{"id": occ.String()}, nil)
		require.Equal(t, fasthttp.StatusOK, c, string(raw))
		assert.Equal(t, fasthttp.StatusForbidden, w.whatsapp(t, ap, occ))
		c, _, _ = w.fileCall(t, w.app.DownloadOccurrenceFile, ap, occ, f.ID.String(), "")
		assert.Equal(t, fasthttp.StatusForbidden, c)
	})
}

func (w *xdWorld) protocolOf(t *testing.T, occ uuid.UUID) string {
	var o models.Occurrence
	require.NoError(t, w.app.DB.First(&o, occ).Error)
	return o.ProtocolNumber
}

func (w *xdWorld) note2(t *testing.T, u *models.User, occ uuid.UUID) int {
	return w.note(t, u, occ, "tentativa")
}

// Three departments on one case: SAC asks, Logística works it, Vendas helps as support, the store
// supervisor oversees. Everyone sees their part, nobody loses information, nobody exceeds permissions.
func TestHomologation_ThreeDepartmentsOnOneCase(t *testing.T) {
	w := newXDWorld(t)
	sac, log, vend := w.u["SAC"], w.u["Log"], w.apoio["Vendas"]
	sup := w.u["SupLoja"]
	occ := w.open(t, sac, w.team["Log"], "Pedido 45872 - três departamentos")
	w.on(occ)

	c, raw := chatDo(t, w.app.AddOccurrenceParticipant, log.OrganizationID, log.ID,
		map[string]any{"user_id": w.apoio["Log"].ID.String(), "role": "support", "department_id": w.dept["Logística"].ID.String()}, map[string]string{"id": occ.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, c, string(raw))
	// the supervisor (who may pick from any department) brings in Vendas
	c, raw = chatDo(t, w.app.AddOccurrenceParticipant, sup.OrganizationID, sup.ID,
		map[string]any{"user_id": vend.ID.String(), "role": "support", "department_id": w.dept["Vendas"].ID.String()}, map[string]string{"id": occ.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, c, string(raw))

	for _, p := range []struct {
		u    *models.User
		text string
	}{{sac, "SAC: cliente ligou de novo"}, {log, "Logística: carga saiu hoje"}, {vend, "Vendas: pedido faturado em 20/10"}, {sup, "Supervisor: acompanhando"}} {
		cc, rr := chatDo(t, w.app.GetOccurrenceChat, p.u.OrganizationID, p.u.ID, nil, map[string]string{"id": occ.String()}, nil)
		require.Equal(t, fasthttp.StatusOK, cc, "%s: %s", p.u.FullName, rr)
		ch := decodeChat[occChatState](t, rr).ChannelID
		cc, rr = chatDo(t, w.app.SendChatMessage, p.u.OrganizationID, p.u.ID, map[string]any{"body": p.text}, map[string]string{"id": ch.String()}, nil)
		require.Equal(t, fasthttp.StatusOK, cc, string(rr))
		w.chID = ch
	}
	for _, u := range []*models.User{sac, log, vend, sup} {
		code, msgs := w.read(t, u.ID)
		require.Equal(t, fasthttp.StatusOK, code, u.FullName)
		assert.Len(t, msgs, 4, "%s sees the whole discussion", u.FullName)
	}
	// Vendas, as support, still has no WhatsApp and no restricted file
	assert.Equal(t, fasthttp.StatusForbidden, w.whatsapp(t, vend, occ))
	f := w.upl2(t, log, occ, "restrito.pdf", pdfBytes())
	cc, _, _ := w.fileCall(t, w.app.DownloadOccurrenceFile, vend, occ, f.ID.String(), "")
	assert.Equal(t, fasthttp.StatusForbidden, cc)

	code, body, _ := w.export(t, log, occ, []string{"full"}, "pdf")
	require.Equal(t, fasthttp.StatusOK, code)
	text := strings.Join(strings.Fields(pdfText(t, body)), " ")
	for _, d := range []string{"SAC", "Logística", "Vendas"} {
		assert.Contains(t, text, w.dept[d].Name, "department %s is in the report", d)
	}
	for _, u := range []*models.User{sac, vend, w.apoio["Log"]} {
		assert.Contains(t, text, u.FullName)
	}
}

// Two people acting at once: the result is one consistent state, never a duplicate or a lost update.
func TestHomologation_ConcurrentWork(t *testing.T) {
	w := newXDWorld(t)
	sac, log, sup := w.u["SAC"], w.u["Log"], w.u["SupLoja"]
	colleague := w.apoio["Log"]
	require.NoError(t, w.app.DB.Create(&models.TeamMember{TeamID: w.team["Log"].ID, UserID: colleague.ID}).Error)
	occ := w.open(t, sac, w.team["Log"], "Pedido 45872 - concorrência")
	w.on(occ)
	sockets := w.hubFor(t, log, colleague, sac)
	_ = sockets

	t.Run("dois assumem o mesmo caso: um só vence", func(t *testing.T) {
		codes := make([]int, 2)
		var wg sync.WaitGroup
		for i, u := range []*models.User{log, colleague} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i], _ = chatDo(t, w.app.ClaimOccurrence, u.OrganizationID, u.ID, map[string]any{}, map[string]string{"id": occ.String()}, nil)
			}()
		}
		wg.Wait()
		assert.ElementsMatch(t, []int{fasthttp.StatusOK, fasthttp.StatusConflict}, codes)
		var o models.Occurrence
		require.NoError(t, w.app.DB.First(&o, occ).Error)
		require.NotNil(t, o.AssignedUserID)
		var n int64
		w.app.DB.Model(&models.OccurrenceEvent{}).Where("occurrence_id = ? AND type = ?", occ, models.OccurrenceEventAssignment).Count(&n)
		assert.LessOrEqual(t, n, int64(1), "one assignment event, not two")
	})

	t.Run("duas mensagens de pessoas diferentes: sem duplicar nem perder", func(t *testing.T) {
		cc, rr := chatDo(t, w.app.GetOccurrenceChat, log.OrganizationID, log.ID, nil, map[string]string{"id": occ.String()}, nil)
		require.Equal(t, fasthttp.StatusOK, cc, string(rr))
		ch := decodeChat[occChatState](t, rr).ChannelID
		w.chID = ch
		var wg sync.WaitGroup
		for _, u := range []*models.User{log, colleague} {
			for i := 0; i < 15; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					chatDo(t, w.app.SendChatMessage, u.OrganizationID, u.ID, map[string]any{"body": u.FullName + " #" + time.Now().Format("150405.000000") + uuid.NewString()[:6]}, map[string]string{"id": ch.String()}, nil)
				}()
			}
		}
		wg.Wait()
		code, msgs := w.read(t, log.ID)
		require.Equal(t, fasthttp.StatusOK, code)
		assert.Len(t, msgs, 30)
		seen := map[string]bool{}
		for _, m := range msgs {
			assert.False(t, seen[m.Body], "duplicate message")
			seen[m.Body] = true
		}
		assert.EqualValues(t, 15, w.state(t, log.ID).Unread, "unread counter = the colleague's 15 messages, not the reader's own")
	})

	t.Run("dois supervisores mudam a etapa ao mesmo tempo", func(t *testing.T) {
		stages := []uuid.UUID{w.stage(t, "waiting_customer"), w.stage(t, "in_progress")}
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i, u := range []*models.User{sup, w.u["SupSAC"]} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i], _ = chatDo(t, w.app.ChangeOccurrenceStage, u.OrganizationID, u.ID, map[string]any{"stage_id": stages[i].String()}, map[string]string{"id": occ.String()}, nil)
			}()
		}
		wg.Wait()
		for _, c := range codes {
			assert.Contains(t, []int{fasthttp.StatusOK, fasthttp.StatusConflict}, c, "no 5xx under a race")
		}
		var o models.Occurrence
		require.NoError(t, w.app.DB.First(&o, occ).Error)
		assert.Contains(t, stages, o.StageID, "the stage is one of the two requested, never a mix")
	})

	t.Run("duas edições do título e duas redistribuições simultâneas", func(t *testing.T) {
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i, title := range []string{"Título A", "Título B"} {
			u := []*models.User{sup, w.u["SupSAC"]}[i]
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i], _ = chatDo(t, w.app.UpdateOccurrence, u.OrganizationID, u.ID, map[string]any{"title": title}, map[string]string{"id": occ.String()}, nil)
			}()
		}
		wg.Wait()
		for _, c := range codes {
			assert.Equal(t, fasthttp.StatusOK, c)
		}
		var o models.Occurrence
		require.NoError(t, w.app.DB.First(&o, occ).Error)
		assert.Contains(t, []string{"Título A", "Título B"}, o.Title, "last write wins, no corruption")

		// concurrent reassign from the same starting owner: only one applies
		var cur models.Occurrence
		require.NoError(t, w.app.DB.First(&cur, occ).Error)
		targets := []*models.User{log, colleague}
		rcodes := make([]int, 2)
		for i, tg := range targets {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rcodes[i], _ = chatDo(t, w.app.ReassignOccurrence, sup.OrganizationID, sup.ID, map[string]any{"assigned_user_id": tg.ID.String()}, map[string]string{"id": occ.String()}, nil)
			}()
		}
		wg.Wait()
		for _, c := range rcodes {
			assert.Contains(t, []int{fasthttp.StatusOK, fasthttp.StatusConflict}, c)
		}
	})

	t.Run("o mesmo apoio adicionado duas vezes ao mesmo tempo", func(t *testing.T) {
		ap := w.apoio["SAC"]
		var wg sync.WaitGroup
		for _, u := range []*models.User{sup, w.u["SupSAC"]} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				chatDo(t, w.app.AddOccurrenceParticipant, u.OrganizationID, u.ID, map[string]any{"user_id": ap.ID.String(), "role": "support", "department_id": w.dept["SAC"].ID.String()}, map[string]string{"id": occ.String()}, nil)
			}()
		}
		wg.Wait()
		var n int64
		w.app.DB.Model(&models.OccurrenceParticipant{}).Where("occurrence_id = ? AND user_id = ?", occ, ap.ID).Count(&n)
		assert.EqualValues(t, 1, n, "one link, not two")
	})
}

// listedFor asks for the list in the person's OWN organization (a user of another organization never reaches ours).
func (w *xdWorld) listedFor(t *testing.T, u *models.User, protocol string) bool {
	code, raw := chatDo(t, w.app.ListOccurrences, u.OrganizationID, u.ID, nil, nil, map[string]string{"search": protocol})
	if code == fasthttp.StatusForbidden { // a profile without protocol access sees no list at all
		return false
	}
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	return strings.Contains(string(raw), protocol)
}

// Regression (found in the browser homologation): with as many simultaneous requests as the pool has connections,
// requests that held a transaction and asked for a second connection froze the server. A tiny pool makes the
// problem appear with a handful of requests.
func TestHomologation_ConcurrentWritesDoNotExhaustTheConnectionPool(t *testing.T) {
	w := newXDWorld(t)
	sac, log, sup := w.u["SAC"], w.u["Log"], w.u["SupLoja"]
	occ := w.open(t, sac, w.team["Log"], "Pedido 45872 - pool")
	w.on(occ)
	cc, rr := chatDo(t, w.app.GetOccurrenceChat, log.OrganizationID, log.ID, nil, map[string]string{"id": occ.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, cc)
	ch := decodeChat[occChatState](t, rr).ChannelID
	w.chID = ch

	sqlDB, err := w.app.DB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	defer sqlDB.SetMaxOpenConns(25)

	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			for _, u := range []*models.User{log, sup} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					chatDo(t, w.app.SendChatMessage, u.OrganizationID, u.ID, map[string]any{"body": u.FullName + " " + uuid.NewString()}, map[string]string{"id": ch.String()}, nil)
				}()
			}
		}
		// simultaneous forwards too (only one can win; none may hang)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				chatDo(t, w.app.ForwardOccurrence, log.OrganizationID, log.ID, map[string]any{"team_id": w.team["Vendas"].ID.String(), "reason": "corrida de encaminhamento"}, map[string]string{"id": occ.String()}, nil)
			}()
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(45 * time.Second):
		t.Fatal("the server froze: concurrent writes exhausted the connection pool")
	}
	code, msgs := w.read(t, sup.ID)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Len(t, msgs, 24, "every message landed exactly once")
}

// Regression (found in the browser): a pushed occurrence update carried no team name, so an open screen lost
// "Equipe" / "Departamento atual" after any stage change by someone else.
func TestHomologation_PushedUpdateCarriesTheTeamName(t *testing.T) {
	w := newXDWorld(t)
	sac, log, sup := w.u["SAC"], w.u["Log"], w.u["SupLoja"]
	occ := w.open(t, sac, w.team["Log"], "Pedido 45872 - push")
	socks := w.hubFor(t, log)
	code, _ := chatDo(t, w.app.ChangeOccurrenceStage, sup.OrganizationID, sup.ID, map[string]any{"stage_id": w.stage(t, "waiting_customer").String()}, map[string]string{"id": occ.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	msg := readBroadcast(t, socks[log.ID], "occurrence_changed")
	b, _ := json.Marshal(msg.Payload)
	assert.Contains(t, string(b), `"team_name":"`+w.team["Log"].Name+`"`)
}
