package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestParticipants_CapabilitiesAreSeparate(t *testing.T) {
	w := newIsoWorld(t)
	id := w.forward(t, nil)
	em := w.stageOf(t, models.GeneralStateInProgress)
	done := w.stageOf(t, models.GeneralStateResolved)
	mv := func(user uuid.UUID, stage models.OccurrenceStage) int {
		c, _ := w.call(t, w.app.ChangeOccurrenceStage, user, id, map[string]any{"stage_id": stage.ID.String()}, nil)
		return c
	}

	// The SAC follows and comments, and only that.
	code, _ := w.call(t, w.app.CreateOccurrenceEvent, w.sac.ID, id, map[string]any{"content": "cliente ligou de novo"}, nil)
	assert.Equal(t, fasthttp.StatusOK, code, "comment")
	code, _ = w.call(t, w.app.ClaimOccurrence, w.sac.ID, id, map[string]any{}, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "claim")
	assert.Equal(t, fasthttp.StatusForbidden, mv(w.sac.ID, em), "change status")
	code, _ = w.call(t, w.app.ReassignOccurrence, w.sac.ID, id, map[string]any{"assigned_user_id": w.logPorto.ID.String()}, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "reassign")
	code, _ = w.call(t, w.app.AddOccurrenceParticipant, w.sac.ID, id, map[string]any{"user_id": w.logOutra.ID.String(), "capabilities": []string{"comment"}}, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "a follower does not invite others")

	// A stranger from another store still cannot comment or even see it.
	code, _ = w.call(t, w.app.CreateOccurrenceEvent, w.logOutra.ID, id, map[string]any{"content": "x"}, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)

	// The receiving agent claims; she may invite a colleague to comment, nothing more.
	code, _ = w.call(t, w.app.ClaimOccurrence, w.logPorto.ID, id, map[string]any{}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	code, _ = w.call(t, w.app.AddOccurrenceParticipant, w.logPorto.ID, id, map[string]any{"user_id": w.logOutra.ID.String(), "capabilities": []string{"resolve"}}, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "only supervisors grant more than comment")
	code, _ = w.call(t, w.app.AddOccurrenceParticipant, w.logPorto.ID, id, map[string]any{"user_id": w.logOutra.ID.String(), "capabilities": []string{"nao_existe"}}, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, code)

	// The store supervisor grants the SAC status changes, then resolve, one right at a time.
	grant := func(caps ...string) int {
		c, _ := w.call(t, w.app.AddOccurrenceParticipant, w.supPorto.ID, id, map[string]any{"user_id": w.sac.ID.String(), "capabilities": caps}, nil)
		return c
	}
	require.Equal(t, fasthttp.StatusOK, grant("comment", "change_status"))
	assert.Equal(t, fasthttp.StatusOK, mv(w.sac.ID, em), "now may change status")
	assert.Equal(t, fasthttp.StatusForbidden, mv(w.sac.ID, done), "but not conclude")
	require.Equal(t, fasthttp.StatusOK, grant("comment", "change_status", "resolve"))
	assert.Equal(t, fasthttp.StatusOK, mv(w.sac.ID, done), "now may conclude")
	assert.Equal(t, fasthttp.StatusOK, mv(w.sac.ID, em), "and reopen")

	// Leaving is the participant's own right.
	code, _ = w.call(t, w.app.RemoveOccurrenceParticipant, w.sac.ID, id, nil, map[string]any{"userId": w.sac.ID.String()})
	assert.Equal(t, fasthttp.StatusOK, code)
	var n int64
	w.app.DB.Model(&models.OccurrenceEvent{}).Where("occurrence_id = ? AND type IN ?", id,
		[]string{string(models.OccurrenceEventParticipantAdded), string(models.OccurrenceEventParticipantRemoved), string(models.OccurrenceEventReopened)}).Count(&n)
	assert.GreaterOrEqual(t, int(n), 4, "added x2, reopened and removed are on the timeline")
}

func TestOccReassign_StaysInsideTheTeamAndIsExclusive(t *testing.T) {
	w := newIsoWorld(t)
	id := w.forward(t, nil)
	mate := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(w.logPorto.RoleID))
	require.NoError(t, w.app.DB.Create(&models.TeamMember{TeamID: w.teamPorto.ID, UserID: mate.ID}).Error)
	code, _ := w.call(t, w.app.ClaimOccurrence, w.logPorto.ID, id, map[string]any{}, nil)
	require.Equal(t, fasthttp.StatusOK, code)

	re := func(by, to uuid.UUID) int {
		c, _ := w.call(t, w.app.ReassignOccurrence, by, id, map[string]any{"assigned_user_id": to.String()}, nil)
		return c
	}
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, re(w.logPorto.ID, w.logOutra.ID), "outside the team")
	assert.Equal(t, fasthttp.StatusForbidden, re(mate.ID, w.logPorto.ID), "a teammate who is not the owner")
	assert.Equal(t, fasthttp.StatusOK, re(w.logPorto.ID, mate.ID), "the owner hands over")
	assert.Equal(t, fasthttp.StatusForbidden, re(w.logPorto.ID, w.logPorto.ID), "no longer the owner")
	assert.Equal(t, fasthttp.StatusOK, re(w.supPorto.ID, w.logPorto.ID), "the supervisor redistributes")

	var o models.Occurrence
	require.NoError(t, w.app.DB.First(&o, id).Error)
	assert.Equal(t, w.logPorto.ID, *o.AssignedUserID)
}

