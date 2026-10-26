package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// "Acompanhamento e apoio": support people picked by department, with the history of the link, and with
// no access beyond what the link grants.

type supportWorld struct {
	*chatWorld
	depLog, depSac, depOther *models.Department
	apoio, inactive, foreign *models.User
}

func newSupportWorld(t *testing.T) *supportWorld {
	t.Helper()
	w := &supportWorld{chatWorld: newFilesWorld(t)}
	w.run()
	org := w.logPorto.OrganizationID
	mk := func(name string) *models.Department {
		d := &models.Department{OrganizationID: org, Name: name + uuid.NewString()[:5], Active: true}
		require.NoError(t, w.app.DB.Create(d).Error)
		return d
	}
	w.depLog, w.depSac, w.depOther = mk("Logística "), mk("SAC "), mk("Financeiro ")
	set := func(u *models.User, d *models.Department) {
		require.NoError(t, w.app.DB.Model(u).Update("department_id", d.ID).Error)
	}
	set(w.logPorto, w.depLog)
	set(w.supPorto, w.depLog)
	set(w.sac, w.depSac)
	// a colleague of Logística with the plain agent profile (the "apoio"), and one that was deactivated
	agent := []string{"occurrences:read", "occurrences:write", "chat:read", "chat:write", "team_chat:read", "team_chat:write", "contacts:read"}
	w.apoio = chatUser(t, w.app, org, agent)
	set(w.apoio, w.depLog)
	w.inactive = chatUser(t, w.app, org, agent)
	set(w.inactive, w.depLog)
	require.NoError(t, w.app.DB.Model(w.inactive).Update("is_active", false).Error)
	for _, u := range []*models.User{w.apoio, w.inactive, w.logPorto, w.supPorto, w.sac, w.supSAC} {
		u.FullName = "Usuario " + uuid.NewString()[:6] // distinct names: the pickers show names
		require.NoError(t, w.app.DB.Model(u).Update("full_name", u.FullName).Error)
	}
	// someone from another organization
	other := newIsoWorld(t)
	w.foreign = chatUser(t, w.app, other.org.ID, chatUserKeys)
	return w
}

func (w *supportWorld) options(t *testing.T, u *models.User, dep string) (int, []byte) {
	t.Helper()
	q := map[string]string{}
	if dep != "" {
		q["department_id"] = dep
	}
	return chatDo(t, w.app.ListSupportOptions, u.OrganizationID, u.ID, nil, map[string]string{"id": w.occID.String()}, q)
}

func (w *supportWorld) add(t *testing.T, by *models.User, body map[string]any) (int, []byte) {
	t.Helper()
	return chatDo(t, w.app.AddOccurrenceParticipant, by.OrganizationID, by.ID, body, map[string]string{"id": w.occID.String()}, nil)
}

func (w *supportWorld) remove(t *testing.T, by *models.User, target uuid.UUID) int {
	t.Helper()
	code, _ := chatDo(t, w.app.RemoveOccurrenceParticipant, by.OrganizationID, by.ID, nil, map[string]string{"id": w.occID.String(), "userId": target.String()}, nil)
	return code
}

type partList struct {
	Data struct {
		Participants []struct {
			UserID         uuid.UUID `json:"user_id"`
			Role           string    `json:"role"`
			DepartmentName string    `json:"department_name"`
			AddedBy        string    `json:"added_by"`
			Active         bool      `json:"active"`
			RemovedBy      string    `json:"removed_by"`
		} `json:"participants"`
	} `json:"data"`
}

func (w *supportWorld) list(t *testing.T, u *models.User) (int, partList) {
	t.Helper()
	code, raw := chatDo(t, w.app.ListOccurrenceParticipants, u.OrganizationID, u.ID, nil, map[string]string{"id": w.occID.String()}, nil)
	var l partList
	_ = json.Unmarshal(raw, &l)
	return code, l
}

func TestSupport_PickersOnlyOfferActiveUsersOfAnAllowedDepartment(t *testing.T) {
	w := newSupportWorld(t)

	// Logística (works the case, not a supervisor of it): its own department, never the others
	code, raw := w.options(t, w.logPorto, "")
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	assert.Contains(t, string(raw), w.depLog.Name)
	assert.NotContains(t, string(raw), w.depOther.Name, "a department the person may not pick from is not even offered")
	code, raw = w.options(t, w.logPorto, w.depLog.ID.String())
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	assert.Contains(t, string(raw), w.apoio.FullName)
	assert.NotContains(t, string(raw), w.inactive.FullName, "only ACTIVE users")
	assert.NotContains(t, string(raw), w.sac.FullName, "only users LINKED to the department")
	code, _ = w.options(t, w.logPorto, w.depOther.ID.String())
	assert.Equal(t, fasthttp.StatusForbidden, code, "a department that is not authorized")

	// a supervisor of the case may pick from any active department
	code, raw = w.options(t, w.supSAC, "")
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	assert.Contains(t, string(raw), w.depOther.Name)

	// no access for a stranger to the case or to another organization
	code, _ = w.options(t, w.foreign, "")
	assert.NotEqual(t, fasthttp.StatusOK, code)
}

func TestSupport_AddRemoveHistoryRolesAndTimeline(t *testing.T) {
	w := newSupportWorld(t)
	dep := w.depLog.ID.String()

	// rules: valid role, active user, user linked to the department, authorized department
	code, _ := w.add(t, w.logPorto, map[string]any{"user_id": w.apoio.ID.String(), "role": "chefe", "department_id": dep})
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	code, _ = w.add(t, w.logPorto, map[string]any{"user_id": w.inactive.ID.String(), "role": "support", "department_id": dep})
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, code, "an inactive user cannot be added")
	code, _ = w.add(t, w.logPorto, map[string]any{"user_id": w.sac.ID.String(), "role": "support", "department_id": dep})
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, code, "the user is not in that department")
	code, _ = w.add(t, w.logPorto, map[string]any{"user_id": w.apoio.ID.String(), "role": "support", "department_id": w.depOther.ID.String()})
	assert.Equal(t, fasthttp.StatusForbidden, code, "a department the caller may not pick from")
	code, _ = w.add(t, w.foreign, map[string]any{"user_id": w.apoio.ID.String(), "role": "support"})
	assert.NotEqual(t, fasthttp.StatusOK, code, "another organization cannot add anyone")
	code, _ = w.add(t, w.logPorto, map[string]any{"user_id": w.foreign.ID.String(), "role": "support"})
	assert.Equal(t, fasthttp.StatusNotFound, code, "nobody from another organization can be added")

	// a support person and a follower
	code, raw := w.add(t, w.logPorto, map[string]any{"user_id": w.apoio.ID.String(), "role": "support", "department_id": dep})
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	code, raw = w.add(t, w.logPorto, map[string]any{"user_id": w.sac.ID.String()})
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	_, l := w.list(t, w.logPorto)
	byUser := map[uuid.UUID]int{}
	for i, p := range l.Data.Participants {
		byUser[p.UserID] = i
	}
	sup := l.Data.Participants[byUser[w.apoio.ID]]
	assert.Equal(t, "support", sup.Role)
	assert.Equal(t, w.depLog.Name, sup.DepartmentName)
	assert.Equal(t, w.logPorto.FullName, sup.AddedBy, "who added")
	assert.True(t, sup.Active)
	assert.Equal(t, "follower", l.Data.Participants[byUser[w.sac.ID]].Role, "the default is to follow")

	// the timeline says who added whom
	var ev models.OccurrenceEvent
	require.NoError(t, w.app.DB.Where("occurrence_id = ? AND type = ?", w.occID, models.OccurrenceEventParticipantAdded).Order("created_at").First(&ev).Error)
	assert.Contains(t, ev.Content, w.logPorto.FullName)
	assert.Contains(t, ev.Content, w.apoio.FullName)
	assert.Contains(t, ev.Content, "apoio")

	// removal keeps the history: who removed, when; the active list no longer carries the person
	require.Equal(t, fasthttp.StatusOK, w.remove(t, w.logPorto, w.apoio.ID))
	_, l = w.list(t, w.logPorto)
	for _, p := range l.Data.Participants {
		if p.UserID == w.apoio.ID {
			assert.False(t, p.Active)
			assert.Equal(t, w.logPorto.FullName, p.RemovedBy)
		}
	}
	var rev models.OccurrenceEvent // a fresh value: a populated primary key would be added to the query
	require.NoError(t, w.app.DB.Where("occurrence_id = ? AND type = ?", w.occID, models.OccurrenceEventParticipantRemoved).First(&rev).Error)
	assert.Contains(t, rev.Content, "removeu")
	assert.Equal(t, fasthttp.StatusNotFound, w.remove(t, w.logPorto, w.apoio.ID), "already removed")
	// a person who is not on the case cannot remove others
	assert.Equal(t, fasthttp.StatusForbidden, w.remove(t, w.apoio, w.sac.ID))

	// adding again revives the link
	code, _ = w.add(t, w.logPorto, map[string]any{"user_id": w.apoio.ID.String(), "role": "support", "department_id": dep})
	require.Equal(t, fasthttp.StatusOK, code)
	_, l = w.list(t, w.logPorto)
	active := 0
	for _, p := range l.Data.Participants {
		if p.UserID == w.apoio.ID && p.Active {
			active++
		}
	}
	assert.Equal(t, 1, active)
}

// Being a support person is not access to the customer's WhatsApp, to restricted files or to other protocols.
func TestSupport_GrantsNoWhatsAppRestrictedFilesOrOtherCases(t *testing.T) {
	w := newSupportWorld(t)
	f := w.upl(t, w.logPorto, "restrito.pdf", pdfBytes()) // restricted by default
	other := w.forward(t, nil)                            // another protocol the support person was never added to

	code, raw := w.add(t, w.logPorto, map[string]any{"user_id": w.apoio.ID.String(), "role": "support", "department_id": w.depLog.ID.String()})
	require.Equal(t, fasthttp.StatusOK, code, string(raw))

	// the case itself opens for the support person (that is what the link is for) ...
	code, _ = chatDo(t, w.app.GetOccurrence, w.apoio.OrganizationID, w.apoio.ID, nil, map[string]string{"id": w.occID.String()}, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
	// ... but not the restricted file, the customer's conversation, or another protocol
	dcode, _, _ := w.fileCall(t, w.app.DownloadOccurrenceFile, w.apoio, w.occID, f.ID.String(), "")
	assert.Equal(t, fasthttp.StatusForbidden, dcode, "restricted file")
	_, raw = w.fileCall2(t, w.app.GetOccurrenceExportOptions, w.apoio, w.occID)
	assert.Contains(t, string(raw), `"whatsapp":false`, "the conversation is not part of what a support person may export")
	code, _ = chatDo(t, w.app.GetOccurrence, w.apoio.OrganizationID, w.apoio.ID, nil, map[string]string{"id": other.String()}, nil)
	assert.NotEqual(t, fasthttp.StatusOK, code, "another protocol")
	_ = testutil.NopLogger
}

func (w *supportWorld) fileCall2(t *testing.T, h func(*fastglue.Request) error, u *models.User, occID uuid.UUID) (int, []byte) {
	t.Helper()
	code, raw, _ := w.fileCall(t, h, u, occID, "", "")
	return code, raw
}
