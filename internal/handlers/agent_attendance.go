package handlers

import (
	"context"
	"sort"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// ---- session tracking (login / refresh / logout) ---------------------------

// jtiOf reads the JTI of a token this server just signed.
func jtiOf(signed string) string {
	c := &middleware.JWTClaims{}
	_, _, _ = jwt.NewParser().ParseUnverified(signed, c)
	return c.ID
}

func sessionKey(jti string) string { return "session_of:" + jti }

func (a *App) refreshLife() time.Duration {
	return time.Duration(a.Config.JWT.RefreshExpiryDays) * 24 * time.Hour
}

// rememberSession maps a (rotated) refresh JTI to its stable session id.
func (a *App) rememberSession(jti, sid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Redis.Set(ctx, sessionKey(jti), sid, a.refreshLife()).Err(); err != nil {
		a.Log.Error("Failed to store session id", "error", err)
	}
}

// sessionOf resolves the stable session id of a refresh JTI (itself if unknown,
// e.g. a session that predates this tracking).
func (a *App) sessionOf(jti string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if sid, err := a.Redis.Get(ctx, sessionKey(jti)).Result(); err == nil && sid != "" {
		return sid
	}
	return jti
}

func (a *App) startSession(r *fastglue.Request, userID, orgID uuid.UUID, refreshToken string) {
	sid := jtiOf(refreshToken)
	a.rememberSession(sid, sid)
	a.setSessionCookie(r, sid)
	a.recordSessionEvent(userID, orgID, "login", sid, "", string(r.RequestCtx.UserAgent()))
}

func (a *App) continueSession(r *fastglue.Request, userID, orgID uuid.UUID, oldJTI, newRefreshToken string) {
	sid := a.sessionOf(oldJTI)
	a.rememberSession(jtiOf(newRefreshToken), sid)
	a.setSessionCookie(r, sid)
	a.recordSessionEvent(userID, orgID, "refresh", sid, "", string(r.RequestCtx.UserAgent()))
}

func (a *App) endSession(r *fastglue.Request, userID, orgID uuid.UUID, jti string) {
	a.recordSessionEvent(userID, orgID, "logout", a.sessionOf(jti), "manual", string(r.RequestCtx.UserAgent()))
}

// endSessionFromCookies records the logout of a browser. The refresh cookie
// never arrives here (its path is /api/auth/refresh), so the session is named by
// its own cookie and the user by the access cookie, whose signature is checked
// but whose expiry is not: logging out with a stale access token is still a
// logout.
func (a *App) endSessionFromCookies(r *fastglue.Request) {
	sid := string(r.RequestCtx.Request.Header.Cookie(cookieSessionName))
	access := string(r.RequestCtx.Request.Header.Cookie(cookieAccessName))
	if sid == "" || access == "" {
		return
	}
	tok, err := jwt.NewParser(jwt.WithoutClaimsValidation()).ParseWithClaims(access, &middleware.JWTClaims{},
		func(*jwt.Token) (any, error) { return []byte(a.Config.JWT.Secret), nil })
	if err != nil || !tok.Valid {
		return
	}
	if cl, ok := tok.Claims.(*middleware.JWTClaims); ok {
		a.recordSessionEvent(cl.UserID, cl.OrganizationID, "logout", sid, "manual", string(r.RequestCtx.UserAgent()))
	}
}

// recordSessionEvent stores a session step. Best effort: a logging failure must
// never block authentication. login/logout are deduplicated per session so a
// retried request cannot double count.
func (a *App) recordSessionEvent(userID, orgID uuid.UUID, event, sessionID, reason, userAgent string) {
	if len(userAgent) > 255 {
		userAgent = userAgent[:255]
	}
	if event != "refresh" {
		var n int64
		a.DB.Model(&models.UserSessionEvent{}).Where("session_id = ? AND event = ?", sessionID, event).Count(&n)
		if n > 0 {
			return
		}
	}
	e := models.UserSessionEvent{UserID: userID, OrganizationID: orgID, Event: event, SessionID: sessionID,
		Reason: reason, UserAgent: userAgent, CreatedAt: time.Now()}
	if err := a.DB.Create(&e).Error; err != nil {
		a.Log.Error("Failed to record session event", "error", err, "event", event)
	}
}

// ---- report ---------------------------------------------------------------

// AttendanceSession is one login session as the report shows it.
type AttendanceSession struct {
	SessionID string     `json:"session_id"`
	LoginAt   time.Time  `json:"login_at"`
	EndAt     *time.Time `json:"end_at,omitempty"`
	// Ended: "manual" (clicked logout), "expired" (no refresh within the token
	// lifetime; end is an estimate) or "active" (still valid).
	Ended     string `json:"ended"`
	Minutes   int    `json:"minutes"`
	UserAgent string `json:"user_agent,omitempty"`
}

// AttendanceInterval is a stretch of one presence state.
type AttendanceInterval struct {
	StartAt time.Time  `json:"start_at"`
	EndAt   *time.Time `json:"end_at,omitempty"` // nil = still in that state
	Minutes int        `json:"minutes"`
	// ClosedReason "restart" means the server restarted: the real end is unknown.
	ClosedReason string `json:"closed_reason,omitempty"`
}

// AgentAttendanceRow is one agent's presence summary for the period.
type AgentAttendanceRow struct {
	UserID        uuid.UUID  `json:"user_id"`
	Name          string     `json:"name"`
	NoLoginRecord bool       `json:"no_login_record"`
	Logins        int        `json:"logins"`
	FirstLogin    *time.Time `json:"first_login,omitempty"`
	LastLogout    *time.Time `json:"last_logout,omitempty"`
	// ActiveSessions counts sessions whose token is still valid. A browser closed
	// without logging out stays "valid" until the token expires, so it can
	// overstate; ConnectedNow (open presence interval) is the truthful live signal.
	ActiveSessions int                 `json:"active_sessions"`
	ConnectedNow   bool                `json:"connected_now"`
	Sessions       []AttendanceSession `json:"sessions"`
	// From the presence log (connection based). Away = connected but marked away.
	ConnectedMinutes int                  `json:"connected_minutes"`
	AwayMinutes      int                  `json:"away_minutes"`
	AwayCount        int                  `json:"away_count"`
	OpenAway         bool                 `json:"open_away"`
	Away             []AttendanceInterval `json:"away"`
	Disconnections   int                  `json:"disconnections"`
}

// sessionsFrom turns raw events into sessions (login inside [start,end]).
func sessionsFrom(events []models.UserSessionEvent, start, end, now time.Time, life time.Duration) (out []AttendanceSession, lastLogout *time.Time, active int) {
	type sess struct {
		login, alive time.Time
		logout       *time.Time
		ua           string
	}
	byID := map[string]*sess{}
	var order []string
	for _, e := range events {
		s := byID[e.SessionID]
		switch e.Event {
		case "login":
			if s == nil {
				byID[e.SessionID] = &sess{login: e.CreatedAt, alive: e.CreatedAt, ua: e.UserAgent}
				order = append(order, e.SessionID)
			}
		case "refresh":
			if s != nil && e.CreatedAt.After(s.alive) {
				s.alive = e.CreatedAt
			}
		case "logout":
			if s != nil && s.logout == nil {
				t := e.CreatedAt
				s.logout = &t
			}
		}
	}
	for _, id := range order {
		s := byID[id]
		if s.login.Before(start) || s.login.After(end) {
			continue
		}
		it := AttendanceSession{SessionID: id, LoginAt: s.login, UserAgent: s.ua}
		var stop time.Time
		switch {
		case s.logout != nil:
			stop, it.Ended = *s.logout, "manual"
			it.EndAt = &stop
			if lastLogout == nil || stop.After(*lastLogout) {
				t := stop
				lastLogout = &t
			}
		case now.After(s.alive.Add(life)):
			stop, it.Ended = s.alive.Add(life), "expired"
			it.EndAt = &stop
		default:
			stop, it.Ended = now, "active"
			active++
		}
		it.Minutes = int(stop.Sub(s.login).Minutes())
		out = append(out, it)
	}
	return
}

// presenceSummary folds presence intervals into connected/away time within
// [start,end]. An interval with no end runs until now.
func presenceSummary(logs []models.AgentPresenceLog, start, end, now time.Time) (connected, awayMin, awayN, disc int, openAway bool, away []AttendanceInterval) {
	away = []AttendanceInterval{}
	for _, l := range logs {
		s, e := l.StartedAt, now
		if l.EndedAt != nil {
			e = *l.EndedAt
		}
		if s.Before(start) {
			s = start
		}
		if e.After(end) {
			e = end
		}
		if !e.After(s) {
			continue
		}
		m := int(e.Sub(s).Minutes())
		switch l.State {
		case models.AgentStateOnline:
			connected += m
		case models.AgentStateAway:
			connected += m
			awayMin += m
			awayN++
			if l.EndedAt == nil {
				openAway = true
			}
			away = append(away, AttendanceInterval{StartAt: l.StartedAt, EndAt: l.EndedAt, Minutes: m, ClosedReason: l.ClosedReason})
		case models.AgentStateOffline:
			if !l.StartedAt.Before(start) {
				disc++ // the drop itself happened inside the period
			}
		}
	}
	return
}

// GetAgentAttendance reports login sessions, away periods and logout per agent.
// Filters: from/to (YYYY-MM-DD, organization timezone), agent_id, team_id,
// unit_id, role_id, only=no_login. Scope follows the live panel: analytics:read
// sees the organization, analytics.agents:view_team only their own teams.
func (a *App) GetAgentAttendance(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	scope := a.liveAgentsScope(userID, orgID)
	if scope == "" {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Access denied", nil, "")
	}
	q := r.RequestCtx.QueryArgs()
	loc := a.orgLocation(orgID)
	now := time.Now()
	y, m, d := now.In(loc).Date()
	start, end := time.Date(y, m, d, 0, 0, 0, 0, loc), now
	if from, to := string(q.Peek("from")), string(q.Peek("to")); from != "" && to != "" {
		f, e1 := time.ParseInLocation("2006-01-02", from, loc)
		t, e2 := time.ParseInLocation("2006-01-02", to, loc)
		if e1 != nil || e2 != nil || t.Before(f) {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid date range. Use YYYY-MM-DD", nil, "")
		}
		start, end = f, t.Add(24*time.Hour-time.Nanosecond)
		if end.After(now) {
			end = now
		}
	}

	users := a.DB.Model(&models.User{}).
		Joins("JOIN user_organizations uo ON uo.user_id = users.id AND uo.organization_id = ? AND uo.deleted_at IS NULL", orgID).
		Where("users.is_active = ?", true)
	if scope == "team" {
		users = users.Where("users.id IN (?)", a.DB.Model(&models.TeamMember{}).Select("user_id").
			Where("team_id IN (?)", a.myTeamIDs(userID)))
	}
	if v := string(q.Peek("agent_id")); v != "" {
		users = users.Where("users.id = ?", v)
	}
	if v := string(q.Peek("role_id")); v != "" {
		users = users.Where("uo.role_id = ?", v)
	}
	orgTeams := a.DB.Model(&models.Team{}).Select("id").Where("organization_id = ?", orgID)
	if v := string(q.Peek("team_id")); v != "" {
		users = users.Where("users.id IN (?)", a.DB.Model(&models.TeamMember{}).Select("user_id").
			Where("team_id = ? AND team_id IN (?)", v, orgTeams))
	}
	if v := string(q.Peek("unit_id")); v != "" {
		users = users.Where("users.id IN (?)", a.DB.Model(&models.TeamMember{}).Select("user_id").
			Where("team_id IN (?)", a.DB.Model(&models.Team{}).Select("id").Where("organization_id = ? AND unit_id = ?", orgID, v)))
	}
	var list []models.User
	if err := users.Find(&list).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load agents", nil, "")
	}

	onlyNoLogin := string(q.Peek("only")) == "no_login"
	out := make([]AgentAttendanceRow, 0, len(list))
	for _, u := range list {
		var evs []models.UserSessionEvent
		// Events from before the window matter too: a session opened earlier may
		// still be alive and its logout can fall inside the window.
		a.DB.Where("user_id = ? AND organization_id = ? AND created_at <= ?", u.ID, orgID, end).
			Order("created_at").Find(&evs)
		var pres []models.AgentPresenceLog
		a.DB.Where("user_id = ? AND organization_id = ? AND started_at <= ? AND (ended_at >= ? OR ended_at IS NULL)",
			u.ID, orgID, end, start).Order("started_at").Find(&pres)

		row := AgentAttendanceRow{UserID: u.ID, Name: u.FullName}
		row.Sessions, row.LastLogout, row.ActiveSessions = sessionsFrom(evs, start, end, now, a.refreshLife())
		if row.Sessions == nil {
			row.Sessions = []AttendanceSession{}
		}
		row.Logins = len(row.Sessions)
		if row.Logins > 0 {
			t := row.Sessions[0].LoginAt
			row.FirstLogin = &t
		}
		row.NoLoginRecord = row.Logins == 0
		for _, p := range pres {
			if p.EndedAt == nil && p.State != models.AgentStateOffline {
				row.ConnectedNow = true
			}
		}
		row.ConnectedMinutes, row.AwayMinutes, row.AwayCount, row.Disconnections, row.OpenAway, row.Away = presenceSummary(pres, start, end, now)
		if onlyNoLogin && !row.NoLoginRecord {
			continue
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return r.SendEnvelope(map[string]any{"agents": out, "scope": scope,
		"period": map[string]any{"from": start, "to": end}})
}
