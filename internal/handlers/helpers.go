package handlers

import (
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"

	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
)

// errEnvelopeSent is a sentinel returned by helpers after they have already
// written an error envelope to the response. Callers should return nil to the framework.
var errEnvelopeSent = errors.New("error envelope sent")

// appLocation is the deployment-wide default timezone a bare "YYYY-MM-DD"
// query param is parsed in when an organization hasn't picked one under
// Configurações → Geral → Fuso horário padrão (org.Settings["timezone"];
// see orgLocation in organization.go). Without a location at all, time.Parse
// defaults to UTC, so "today" typed by a Bahia-based user silently excluded
// anything that happened after ~21:00 local time from any period filter —
// found while verifying the SAC dashboard widgets.
//
// America/Bahia has no DST (Brazil dropped it in 2019), so the FixedZone
// fallback is not an approximation — it's the fixed offset LoadLocation
// would also always return, kept only in case the tzdata package isn't
// available in a given runtime.
var appLocation = func() *time.Location {
	loc, err := time.LoadLocation("America/Bahia")
	if err != nil {
		return time.FixedZone("America/Bahia", -3*60*60)
	}
	return loc
}()

// parsePathUUID extracts a UUID from a path parameter. On failure, it sends a
// 400 error envelope and returns uuid.Nil plus an error.
func parsePathUUID(r *fastglue.Request, param, label string) (uuid.UUID, error) {
	idStr, _ := r.RequestCtx.UserValue(param).(string)
	id, err := uuid.Parse(idStr)
	if err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid "+label+" ID", nil, "")
		return uuid.Nil, errEnvelopeSent
	}
	return id, nil
}

// Pagination holds parsed pagination parameters.
type Pagination struct {
	Page   int
	Limit  int
	Offset int
}

// Apply adds Offset and Limit to a GORM query.
func (pg Pagination) Apply(query *gorm.DB) *gorm.DB {
	return query.Offset(pg.Offset).Limit(pg.Limit)
}

// parsePagination extracts page-based pagination from query params with
// default limit=50 and max limit=100.
func parsePagination(r *fastglue.Request) Pagination {
	return parsePaginationWithDefaults(r, 50, 100)
}

// parsePaginationWithDefaults extracts page-based pagination with custom defaults.
func parsePaginationWithDefaults(r *fastglue.Request, defaultLimit, maxLimit int) Pagination {
	page, _ := strconv.Atoi(string(r.RequestCtx.QueryArgs().Peek("page")))
	limit, _ := strconv.Atoi(string(r.RequestCtx.QueryArgs().Peek("limit")))

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > maxLimit {
		limit = defaultLimit
	}
	return Pagination{
		Page:   page,
		Limit:  limit,
		Offset: (page - 1) * limit,
	}
}

// parseDateParam parses a YYYY-MM-DD date from the named query parameter.
// Returns the parsed time and true on success, or zero time and false if the
// parameter is missing or malformed.
func parseDateParam(r *fastglue.Request, param string, loc *time.Location) (time.Time, bool) {
	s := string(r.RequestCtx.QueryArgs().Peek(param))
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// endOfDay returns the last nanosecond of the given day.
func endOfDay(t time.Time) time.Time {
	return t.Add(24*time.Hour - time.Nanosecond)
}

// findByIDAndOrg fetches a single record scoped by ID and organization.
// Sends a 404 error envelope on failure and returns the error.
func findByIDAndOrg[T any](db *gorm.DB, r *fastglue.Request, id, orgID uuid.UUID, label string) (*T, error) {
	var model T
	if err := db.Where("id = ? AND organization_id = ?", id, orgID).First(&model).Error; err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, label+" not found", nil, "")
		return nil, errEnvelopeSent
	}
	return &model, nil
}

// logAudit records an audit-log entry for a resource mutation, resolving the
// actor's display name automatically. It wraps audit.LogAudit to remove the
// repeated a.DB + GetUserName boilerplate at call sites.
func (a *App) logAudit(orgID, userID uuid.UUID, resourceType string, resourceID uuid.UUID, action models.AuditAction, oldData, newData any, extraChanges ...map[string]any) {
	audit.LogAudit(a.DB, orgID, userID, audit.GetUserName(a.DB, userID), resourceType, resourceID, action, oldData, newData, extraChanges...)
}

// listEnvelope builds the standard paginated list response payload used across
// list handlers: {<key>: items, total, page, limit}.
func listEnvelope(key string, items, total any, pg Pagination) map[string]any {
	return map[string]any{
		key:     items,
		"total": total,
		"page":  pg.Page,
		"limit": pg.Limit,
	}
}

// parseDateRange parses start and end date strings in YYYY-MM-DD format.
// Applies end-of-day to the end date. Returns an error message suitable for
// display if parsing fails.
func parseDateRange(startStr, endStr string, loc *time.Location) (start, end time.Time, errMsg string) {
	var err error
	start, err = time.ParseInLocation("2006-01-02", startStr, loc)
	if err != nil {
		return time.Time{}, time.Time{}, "Invalid start date format. Use YYYY-MM-DD"
	}
	end, err = time.ParseInLocation("2006-01-02", endStr, loc)
	if err != nil {
		return time.Time{}, time.Time{}, "Invalid end date format. Use YYYY-MM-DD"
	}
	end = endOfDay(end)
	return start, end, ""
}
