package handlers

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// The full history of a case, paged. Newest first, keyset cursor on (created_at, id): a page
// never shifts when new events arrive, and nothing is loaded that was not asked for.
//
// What a person sees follows what they may read elsewhere: the text of messages sent to the
// customer (reply, process_message_used) is the WhatsApp conversation, so it is shown only to
// those who may read that conversation (canUseCaseChannel); everyone else gets the fact that it
// happened, with the text withheld. A stage change that is only the other half of closing or
// reopening the case (same person, same moment) is not listed twice.

const (
	historyDefaultLimit = 30
	historyMaxLimit     = 100
	historySearchMax    = 100
)

// historyChannelTypes carry the customer conversation's text.
var historyChannelTypes = []string{string(models.OccurrenceEventReply), string(models.OccurrenceEventProcessMessageUsed)}

var historyKnownTypes = map[string]bool{
	"opened": true, "note": true, "reply": true, "stage_change": true, "assignment": true, "protocol_sent": true,
	"closed": true, "forwarded": true, "participant_added": true, "participant_removed": true, "reopened": true,
	"scope_regularized": true, "customer_notification": true, "process_message_used": true, "file_added": true, "file_removed": true,
}

func encodeHistoryCursor(t time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeHistoryCursor(s string) (time.Time, uuid.UUID, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, false
	}
	t, terr := time.Parse(time.RFC3339Nano, parts[0])
	id, ierr := uuid.Parse(parts[1])
	return t, id, terr == nil && ierr == nil
}

// ListOccurrenceHistory returns one page of the case history.
//
//	GET /api/occurrences/{id}/history?limit=30&cursor=<opaque>&types=note,reply&q=text
func (a *App) ListOccurrenceHistory(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	args := r.RequestCtx.QueryArgs()
	limit := historyDefaultLimit
	if n, perr := strconv.Atoi(string(args.Peek("limit"))); perr == nil && n > 0 {
		limit = min(n, historyMaxLimit)
	}
	account := a.caseChannelAccount(orgID, occ)
	readsChannel := a.canUseCaseChannel(orgID, userID, occ, acc, account, false)

	q := a.DB.Model(&models.OccurrenceEvent{}).Where("organization_id = ? AND occurrence_id = ?", orgID, occ.ID).
		Where(`NOT (type = 'stage_change' AND EXISTS (
			SELECT 1 FROM occurrence_events s
			WHERE s.occurrence_id = occurrence_events.occurrence_id AND s.deleted_at IS NULL
			  AND s.type IN ('closed','reopened')
			  AND s.created_by_id IS NOT DISTINCT FROM occurrence_events.created_by_id
			  AND ABS(EXTRACT(EPOCH FROM (s.created_at - occurrence_events.created_at))) < 2))`)

	if raw := strings.TrimSpace(string(args.Peek("types"))); raw != "" {
		var types []string
		for _, t := range strings.Split(raw, ",") {
			if t = strings.TrimSpace(t); historyKnownTypes[t] {
				types = append(types, t)
			}
		}
		if len(types) == 0 {
			return r.SendEnvelope(map[string]any{"events": []OccurrenceEventResponse{}, "has_more": false, "next_cursor": ""})
		}
		q = q.Where("type IN ?", types)
	}
	if text := strings.TrimSpace(string(args.Peek("q"))); text != "" {
		if len([]rune(text)) > historySearchMax {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Search text is too long", nil, "")
		}
		like := "%" + escapeLike(strings.ToLower(text)) + "%"
		if readsChannel {
			q = q.Where("LOWER(content) LIKE ?", like)
		} else {
			// The text of the customer conversation is not searchable by someone who cannot read it.
			q = q.Where("LOWER(content) LIKE ? AND type NOT IN ?", like, historyChannelTypes)
		}
	}
	if c := string(args.Peek("cursor")); c != "" {
		at, id, ok := decodeHistoryCursor(c)
		if !ok {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid cursor", nil, "")
		}
		q = q.Where("(created_at, id) < (?, ?)", at, id)
	}

	var events []models.OccurrenceEvent
	if err := q.Preload("CreatedBy").Order("created_at DESC, id DESC").Limit(limit + 1).Find(&events).Error; err != nil {
		a.Log.Error("Failed to list occurrence history", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list history", nil, "")
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	out := make([]OccurrenceEventResponse, len(events))
	for i, e := range events {
		out[i] = OccurrenceEventResponse{
			ID: e.ID, OccurrenceID: e.OccurrenceID, Type: string(e.Type), Content: e.Content,
			Metadata: e.Metadata, CreatedByID: e.CreatedByID, CreatedAt: e.CreatedAt,
		}
		if e.CreatedBy != nil {
			out[i].CreatedByName = e.CreatedBy.FullName
		}
		if !readsChannel && (e.Type == models.OccurrenceEventReply || e.Type == models.OccurrenceEventProcessMessageUsed) {
			out[i].Content, out[i].Metadata, out[i].Redacted = "", nil, true
		}
	}
	next := ""
	if hasMore && len(events) > 0 {
		last := events[len(events)-1]
		next = encodeHistoryCursor(last.CreatedAt, last.ID)
	}
	return r.SendEnvelope(map[string]any{"events": out, "has_more": hasMore, "next_cursor": next})
}
