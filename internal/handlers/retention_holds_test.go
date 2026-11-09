package handlers_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Legal holds: placing, revoking, history, isolation, and their effect on retention and on deletion.

type holdResp struct {
	Data struct {
		ID     uuid.UUID `json:"id"`
		Active bool      `json:"active"`
	} `json:"data"`
}

func (w *retWorld) placeHold(t *testing.T, u *models.User, body map[string]any) (int, uuid.UUID) {
	t.Helper()
	code, raw := chatDo(t, w.app.CreateRetentionHold, u.OrganizationID, u.ID, body, nil, nil)
	var r holdResp
	_ = json.Unmarshal(raw, &r)
	return code, r.Data.ID
}

func (w *retWorld) revokeHold(t *testing.T, u *models.User, id uuid.UUID, reason string) int {
	t.Helper()
	code, _ := chatDo(t, w.app.RevokeRetentionHold, u.OrganizationID, u.ID, map[string]any{"reason": reason}, map[string]string{"id": id.String()}, nil)
	return code
}

func (w *retWorld) protocolOf(t *testing.T, occ uuid.UUID) (string, uuid.UUID) {
	t.Helper()
	var o models.Occurrence
	require.NoError(t, w.app.DB.First(&o, "id = ?", occ).Error)
	return o.ProtocolNumber, o.ContactID
}

func TestRetention_Holds_PlaceRevokeHistoryAndIsolation(t *testing.T) {
	w := newRetWorld(t)
	number, contactID := w.protocolOf(t, w.occID)
	f := w.upl(t, w.logPorto, "sob-suspensao.pdf", pdfBytes())
	w.closeCase(t, w.occID, 60)
	require.Equal(t, fasthttp.StatusOK, w.setPolicy(t, w.admin, models.RetentionOccurrenceFiles, 12, true))
	elig := func() int64 {
		_, sim := w.simulate(t, w.admin, map[string]any{"categories": []string{models.RetentionOccurrenceFiles}})
		return sim[models.RetentionOccurrenceFiles].Buckets["eligible"].Count
	}
	require.EqualValues(t, 1, elig())

	reason := "Litígio em andamento com o cliente"
	code, _ := w.placeHold(t, w.viewer, map[string]any{"scope_type": "occurrence", "protocol_number": number, "reason": reason})
	assert.Equal(t, fasthttp.StatusForbidden, code)
	code, _ = w.placeHold(t, w.admin, map[string]any{"scope_type": "occurrence", "protocol_number": number, "reason": "curto"})
	assert.Equal(t, fasthttp.StatusBadRequest, code, "no hold without a real justification")
	code, _ = w.placeHold(t, w.admin, map[string]any{"scope_type": "occurrence", "protocol_number": "9999-999999", "reason": reason})
	assert.Equal(t, fasthttp.StatusNotFound, code)
	code, _ = w.placeHold(t, w.admin, map[string]any{"scope_type": "folder", "scope_id": uuid.NewString(), "reason": reason})
	assert.Equal(t, fasthttp.StatusBadRequest, code)

	// another organization cannot place a hold on our protocol, nor see ours
	other := newIsoWorld(t)
	oadmin := chatUser(t, w.app, other.org.ID, append([]string{"settings.general:read", "settings.general:write"}, chatUserKeys...))
	code, _ = w.placeHold(t, oadmin, map[string]any{"scope_type": "occurrence", "protocol_number": number, "reason": "Tentativa de outra organização"})
	assert.Equal(t, fasthttp.StatusNotFound, code)

	// the hold takes effect at once; a second one for the same scope is refused
	code, holdID := w.placeHold(t, w.admin, map[string]any{"scope_type": "occurrence", "protocol_number": number, "reason": reason})
	require.Equal(t, fasthttp.StatusOK, code)
	assert.EqualValues(t, 0, elig(), "a protocol under hold is never eligible")
	_, sim := w.simulate(t, w.admin, map[string]any{"categories": []string{models.RetentionOccurrenceFiles}})
	assert.EqualValues(t, 1, sim[models.RetentionOccurrenceFiles].Buckets["held"].Count)
	code, _ = w.placeHold(t, w.admin, map[string]any{"scope_type": "occurrence", "protocol_number": number, "reason": "Outra justificativa qualquer"})
	assert.Equal(t, fasthttp.StatusConflict, code)

	// the ordinary delete action refuses too, and the file is still there
	delCode, _, _ := w.fileCall(t, w.app.DeleteOccurrenceFile, w.logPorto, w.occID, f.ID.String(), "")
	assert.Equal(t, fasthttp.StatusLocked, delCode)
	assert.Equal(t, fasthttp.StatusOK, w.dl(t, w.logPorto, f.ID, ""))

	// visibility: ours only
	_, raw := chatDo(t, w.app.ListRetentionHolds, w.admin.OrganizationID, w.admin.ID, nil, nil, nil)
	assert.Contains(t, string(raw), "Litígio em andamento")
	assert.Contains(t, string(raw), `"active":true`)
	_, raw = chatDo(t, w.app.ListRetentionHolds, other.org.ID, oadmin.ID, nil, nil, nil)
	assert.NotContains(t, string(raw), "Litígio")
	assert.Equal(t, fasthttp.StatusNotFound, w.revokeHold(t, oadmin, holdID, "Tentando revogar de outra organização"), "another organization cannot revoke it")

	// revocation: authorized, justified, once
	assert.Equal(t, fasthttp.StatusForbidden, w.revokeHold(t, w.viewer, holdID, "Litígio encerrado com acordo"))
	assert.Equal(t, fasthttp.StatusBadRequest, w.revokeHold(t, w.admin, holdID, "ok"))
	require.Equal(t, fasthttp.StatusOK, w.revokeHold(t, w.admin, holdID, "Litígio encerrado com acordo"))
	assert.Equal(t, fasthttp.StatusConflict, w.revokeHold(t, w.admin, holdID, "Litígio encerrado com acordo"), "revoking twice")
	assert.EqualValues(t, 1, elig(), "after the revocation the file is eligible again")
	var h models.RetentionHold
	require.NoError(t, w.app.DB.First(&h, "id = ?", holdID).Error, "a hold is never deleted")
	require.NotNil(t, h.RevokedAt)
	assert.Equal(t, w.admin.ID, *h.RevokedByID)
	assert.Equal(t, "Litígio encerrado com acordo", h.RevokeReason)

	// the history keeps both events, in order, with the reasons
	var evs []models.RetentionHoldEvent
	require.NoError(t, w.app.DB.Where("hold_id = ?", holdID).Order("created_at").Find(&evs).Error)
	require.Len(t, evs, 2)
	assert.Equal(t, "placed", evs[0].Action)
	assert.Equal(t, "revoked", evs[1].Action)
	_, raw = chatDo(t, w.app.RetentionHistory, w.admin.OrganizationID, w.admin.ID, nil, nil, nil)
	assert.Contains(t, string(raw), "hold_placed")
	assert.Contains(t, string(raw), "hold_revoked")
	require.Eventually(t, func() bool {
		var n int64
		w.app.DB.Model(&models.AuditLog{}).Where("organization_id = ? AND resource_type = ?", w.admin.OrganizationID, "retention_hold").Count(&n)
		return n >= 2
	}, 3*time.Second, 50*time.Millisecond, "placing and revoking are audited")

	// a hold on the CONTACT covers that contact's protocols
	code, cHold := w.placeHold(t, w.admin, map[string]any{"scope_type": "contact", "scope_id": contactID.String(), "reason": "Obrigação de guarda do contato"})
	require.Equal(t, fasthttp.StatusOK, code)
	assert.EqualValues(t, 0, elig())
	require.Equal(t, fasthttp.StatusOK, w.revokeHold(t, w.admin, cHold, "Obrigação de guarda encerrada"))
	assert.EqualValues(t, 1, elig())
}

// The automatic cleanup that already existed (files of deleted chat messages) also respects a hold on the case.
func TestRetention_Holds_CaseDiscussionCleanupIsSuspended(t *testing.T) {
	w := newRetWorld(t)
	org := w.logPorto.OrganizationID
	root := w.app.Config.Storage.LocalPath
	attID := uuid.New() // stored files are named after their attachment: anything else is taken for an interrupted upload
	rel := filepath.Join("team-chat", org.String(), w.chID.String(), attID.String()+".pdf")
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, rel), pdfBytes(), 0o644))
	msg := &models.ChatMessage{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, ChannelID: w.chID, UserID: w.logPorto.ID, Body: "x"}
	require.NoError(t, w.app.DB.Create(msg).Error)
	att := &models.ChatAttachment{ID: attID, OrganizationID: org, ChannelID: w.chID, UploaderID: w.logPorto.ID, MessageID: &msg.ID, Kind: "file",
		Name: "doc.pdf", Mime: "application/pdf", SizeBytes: 10, SHA256: "x", StoragePath: rel}
	require.NoError(t, w.app.DB.Create(att).Error)
	require.NoError(t, w.app.DB.Delete(msg).Error) // the person deleted the message

	number, _ := w.protocolOf(t, w.occID)
	code, hold := w.placeHold(t, w.admin, map[string]any{"scope_type": "occurrence", "protocol_number": number, "reason": "Provas do atendimento em disputa"})
	require.Equal(t, fasthttp.StatusOK, code)

	later := time.Now().Add(30 * 24 * time.Hour)
	w.app.CleanupChatAttachments(later)
	_, err := os.Stat(filepath.Join(root, rel))
	assert.NoError(t, err, "the file of a deleted message in a case under hold is kept")
	var still models.ChatAttachment
	require.NoError(t, w.app.DB.First(&still, "id = ?", att.ID).Error)
	assert.Nil(t, still.PurgedAt)

	require.Equal(t, fasthttp.StatusOK, w.revokeHold(t, w.admin, hold, "Disputa encerrada sem pendências"))
	w.app.CleanupChatAttachments(later)
	_, err = os.Stat(filepath.Join(root, rel))
	assert.True(t, os.IsNotExist(err), "without the hold the existing cleanup proceeds as it always did")
}
