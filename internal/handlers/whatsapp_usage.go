package handlers

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// Read side of the WhatsApp consumption ledger: a summary, a paginated list and
// the reprice action. Nothing here returns message content or phone numbers, and
// every query is scoped by organization. Amounts are never added across
// currencies: totals come back per currency.

type usageFilter struct {
	from, to  time.Time
	account   string
	unitID    *uuid.UUID
	agentID   *uuid.UUID
	category  string
	direction string
	state     string
	actor     string
}

// parseUsageFilter reads the filters from the query string. The period is in the
// organization's timezone and defaults to the last 30 days.
func (a *App) parseUsageFilter(r *fastglue.Request, orgID uuid.UUID) (usageFilter, *time.Location, string) {
	loc := a.orgLocation(orgID)
	q := r.RequestCtx.QueryArgs()
	f := usageFilter{
		account:   string(q.Peek("account")),
		category:  strings.ToLower(string(q.Peek("category"))),
		direction: string(q.Peek("direction")),
		state:     string(q.Peek("billing_state")),
		actor:     string(q.Peek("actor_type")),
	}
	today := time.Now().In(loc)
	f.from = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -29)
	f.to = endOfDay(time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc))
	fromStr, toStr := string(q.Peek("from")), string(q.Peek("to"))
	if fromStr != "" || toStr != "" {
		if fromStr == "" || toStr == "" {
			return f, loc, "from and to must be given together"
		}
		var msg string
		f.from, f.to, msg = parseDateRange(fromStr, toStr, loc)
		if msg != "" {
			return f, loc, msg
		}
		if f.to.Before(f.from) {
			return f, loc, "to cannot be before from"
		}
	}
	for _, p := range []struct {
		name string
		dst  **uuid.UUID
	}{{"unit_id", &f.unitID}, {"agent_id", &f.agentID}} {
		if s := string(q.Peek(p.name)); s != "" {
			id, err := uuid.Parse(s)
			if err != nil {
				return f, loc, "invalid " + p.name
			}
			*p.dst = &id
		}
	}
	return f, loc, ""
}

// where builds the shared WHERE clause (over the table name message_usage).
func (f usageFilter) where(orgID uuid.UUID) (string, []any) {
	w := []string{"message_usage.organization_id = ?", "message_usage.sent_at >= ?", "message_usage.sent_at <= ?"}
	args := []any{orgID, f.from, f.to}
	add := func(cond string, v any) {
		w = append(w, cond)
		args = append(args, v)
	}
	if f.account != "" {
		add("message_usage.whatsapp_account = ?", f.account)
	}
	if f.unitID != nil {
		add("message_usage.unit_id = ?", *f.unitID)
	}
	if f.agentID != nil {
		add("message_usage.actor_user_id = ?", *f.agentID)
	}
	switch f.category {
	case "":
	case "unknown":
		w = append(w, "message_usage.billing_category = ''")
	default:
		add("message_usage.billing_category = ?", f.category)
	}
	if f.direction != "" {
		add("message_usage.direction = ?", f.direction)
	}
	if f.state != "" {
		add("message_usage.billing_state = ?", f.state)
	}
	if f.actor != "" {
		add("message_usage.actor_type = ?", f.actor)
	}
	return strings.Join(w, " AND "), args
}

type usageCounts struct {
	Total             int64 `gorm:"column:total" json:"total"`
	Billed            int64 `gorm:"column:billed" json:"billed"` // priced
	NotBillable       int64 `gorm:"column:not_billable" json:"not_billable"`
	AwaitingPricing   int64 `gorm:"column:awaiting_pricing" json:"awaiting_pricing"`
	NoRate            int64 `gorm:"column:no_rate" json:"no_rate"`
	Unconfirmed       int64 `gorm:"column:unconfirmed" json:"unconfirmed"`
	Failed            int64 `gorm:"column:failed" json:"failed"`
	SendFailed        int64 `gorm:"column:send_failed" json:"send_failed"`
	Pending           int64 `gorm:"column:pending" json:"pending"`
	Unclassified      int64 `gorm:"column:unclassified" json:"unclassified"`
	Unlinked          int64 `gorm:"column:unlinked" json:"unlinked"`
	UnlinkedAttention int64 `gorm:"column:unlinked_attention" json:"unlinked_attention"`
}

type currencyAmount struct {
	Currency      string  `gorm:"column:currency" json:"currency"`
	EstimatedCost float64 `gorm:"column:cost" json:"estimated_cost"`
	Messages      int64   `gorm:"column:messages" json:"messages"`
}

type usageGroup struct {
	Key      string           `json:"key"`
	Label    string           `json:"label,omitempty"`
	Messages int64            `json:"messages"`
	Billed   int64            `json:"billed"`
	Costs    []currencyAmount `json:"costs"`
}

// groupExpr returns the SQL expression of a group_by key (whitelisted).
func groupExpr(by string, loc *time.Location) (string, []any, bool) {
	switch by {
	case "day":
		return "to_char(message_usage.sent_at AT TIME ZONE ?, 'YYYY-MM-DD')", []any{loc.String()}, true
	case "category":
		return "COALESCE(NULLIF(message_usage.billing_category, ''), 'unknown')", nil, true
	case "unit":
		return "COALESCE(message_usage.unit_id::text, '')", nil, true
	case "agent":
		return "COALESCE(message_usage.actor_user_id::text, '')", nil, true
	case "account":
		return "message_usage.whatsapp_account", nil, true
	case "actor":
		return "message_usage.actor_type", nil, true
	}
	return "", nil, false
}

// GetWhatsAppUsageSummary returns the totals of the period, per currency.
func (a *App) GetWhatsAppUsageSummary(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceWhatsAppUsage, models.ActionRead)
	if err != nil {
		return nil
	}
	f, loc, msg := a.parseUsageFilter(r, orgID)
	if msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}
	where, args := f.where(orgID)
	fail := func(e error) error {
		a.Log.Error("Failed to load WhatsApp usage summary", "error", e)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load the consumption", nil, "")
	}

	var counts usageCounts
	attention := time.Now().Add(-a.usageAttentionAfter())
	if err := a.DB.Raw(`
		SELECT count(*) AS total,
		  count(*) FILTER (WHERE billing_state = 'priced') AS billed,
		  count(*) FILTER (WHERE billing_state = 'not_billable') AS not_billable,
		  count(*) FILTER (WHERE billing_state = 'awaiting_pricing') AS awaiting_pricing,
		  count(*) FILTER (WHERE billing_state = 'no_rate') AS no_rate,
		  count(*) FILTER (WHERE billing_state = 'unconfirmed') AS unconfirmed,
		  count(*) FILTER (WHERE billing_state = 'failed') AS failed,
		  count(*) FILTER (WHERE billing_state = 'send_failed') AS send_failed,
		  count(*) FILTER (WHERE billing_state = 'pending') AS pending,
		  count(*) FILTER (WHERE origin_inferred AND origin_detail = 'unclassified') AS unclassified,
		  count(*) FILTER (WHERE link_state = 'unlinked') AS unlinked,
		  count(*) FILTER (WHERE link_state = 'unlinked' AND created_at <= ?) AS unlinked_attention
		FROM message_usage WHERE `+where, append([]any{attention}, args...)...).Scan(&counts).Error; err != nil {
		return fail(err)
	}

	var costs []currencyAmount
	if err := a.DB.Raw(`
		SELECT estimated_currency AS currency, COALESCE(SUM(estimated_cost), 0) AS cost, count(*) AS messages
		FROM message_usage
		WHERE `+where+` AND estimated_cost IS NOT NULL AND estimated_currency <> ''
		GROUP BY estimated_currency ORDER BY estimated_currency`, args...).Scan(&costs).Error; err != nil {
		return fail(err)
	}

	// Provisional cost of the messages Meta delivered without pricing: priced by the
	// category the template declared. Computed here, never stored, and kept apart.
	var provisional []currencyAmount
	if err := a.DB.Raw(`
		SELECT rt.currency AS currency, COALESCE(SUM(rt.price * message_usage.quantity), 0) AS cost, count(*) AS messages
		FROM message_usage
		JOIN LATERAL (
		  SELECT price, currency FROM whatsapp_rates w
		  WHERE w.organization_id = message_usage.organization_id
		    AND w.category = lower(message_usage.declared_category)
		    AND w.country IN (message_usage.recipient_country, '*')
		    AND w.valid_from <= (message_usage.sent_at AT TIME ZONE 'UTC')::date
		    AND (w.valid_to IS NULL OR w.valid_to >= (message_usage.sent_at AT TIME ZONE 'UTC')::date)
		  ORDER BY CASE WHEN w.country = '*' THEN 1 ELSE 0 END, w.valid_from DESC LIMIT 1
		) rt ON true
		WHERE `+where+` AND message_usage.billing_state = 'awaiting_pricing' AND message_usage.declared_category <> ''
		GROUP BY rt.currency ORDER BY rt.currency`, args...).Scan(&provisional).Error; err != nil {
		return fail(err)
	}

	// Activation state: the screen must say whether measurement is on and since when,
	// instead of letting an empty table be mistaken for "no consumption".
	var since *time.Time
	_ = a.DB.Model(&models.MessageUsage{}).Where("organization_id = ?", orgID).Select("MIN(created_at)").Row().Scan(&since)

	resp := map[string]any{
		"recording":                map[string]any{"enabled": a.Usage.Enabled(), "since": since},
		"period":                   map[string]any{"from": f.from.Format("2006-01-02"), "to": f.to.Format("2006-01-02"), "timezone": loc.String()},
		"counts":                   counts,
		"costs":                    nonNil(costs),
		"provisional_cost":         nonNil(provisional),
		"unlinked_attention_hours": int(a.usageAttentionAfter().Hours()),
	}

	if by := string(r.RequestCtx.QueryArgs().Peek("group_by")); by != "" {
		expr, exprArgs, ok := groupExpr(by, loc)
		if !ok {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "group_by must be one of day, category, unit, agent, account, actor", nil, "")
		}
		groups, err := a.usageGroups(orgID, by, expr, exprArgs, where, args)
		if err != nil {
			return fail(err)
		}
		resp["groups"] = groups
	}
	return r.SendEnvelope(resp)
}

func nonNil(v []currencyAmount) []currencyAmount {
	if v == nil {
		return []currencyAmount{}
	}
	return v
}

// usageAttentionAfter is the age from which an `unlinked` row is highlighted.
func (a *App) usageAttentionAfter() time.Duration {
	if a.Config == nil {
		return 24 * time.Hour
	}
	return a.Config.Usage.UnlinkedAttentionAfter()
}

func (a *App) usageGroups(orgID uuid.UUID, by, expr string, exprArgs []any, where string, args []any) ([]usageGroup, error) {
	type countRow struct {
		Key      string `gorm:"column:k"`
		Messages int64  `gorm:"column:messages"`
		Billed   int64  `gorm:"column:billed"`
	}
	var counts []countRow
	if err := a.DB.Raw(`SELECT `+expr+` AS k, count(*) AS messages, count(*) FILTER (WHERE billing_state = 'priced') AS billed
		FROM message_usage WHERE `+where+` GROUP BY k ORDER BY k`, append(append([]any{}, exprArgs...), args...)...).Scan(&counts).Error; err != nil {
		return nil, err
	}
	type costRow struct {
		Key      string  `gorm:"column:k"`
		Currency string  `gorm:"column:currency"`
		Cost     float64 `gorm:"column:cost"`
		Messages int64   `gorm:"column:messages"`
	}
	var costRows []costRow
	if err := a.DB.Raw(`SELECT `+expr+` AS k, estimated_currency AS currency, COALESCE(SUM(estimated_cost), 0) AS cost, count(*) AS messages
		FROM message_usage WHERE `+where+` AND estimated_cost IS NOT NULL AND estimated_currency <> ''
		GROUP BY k, estimated_currency ORDER BY k, estimated_currency`, append(append([]any{}, exprArgs...), args...)...).Scan(&costRows).Error; err != nil {
		return nil, err
	}
	groups := make([]usageGroup, 0, len(counts))
	index := map[string]int{}
	for _, c := range counts {
		index[c.Key] = len(groups)
		groups = append(groups, usageGroup{Key: c.Key, Messages: c.Messages, Billed: c.Billed, Costs: []currencyAmount{}})
	}
	for _, c := range costRows {
		if i, ok := index[c.Key]; ok {
			groups[i].Costs = append(groups[i].Costs, currencyAmount{Currency: c.Currency, EstimatedCost: c.Cost, Messages: c.Messages})
		}
	}
	a.labelUsageGroups(orgID, by, groups)
	if by == "day" { // oldest first reads naturally; the SQL already sorts by key
		sort.SliceStable(groups, func(i, j int) bool { return groups[i].Key < groups[j].Key })
	}
	return groups, nil
}

// labelUsageGroups resolves unit and agent ids to names (within the organization).
func (a *App) labelUsageGroups(orgID uuid.UUID, by string, groups []usageGroup) {
	if by != "unit" && by != "agent" {
		return
	}
	var ids []uuid.UUID
	for _, g := range groups {
		if id, err := uuid.Parse(g.Key); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	type nameRow struct {
		ID   uuid.UUID `gorm:"column:id"`
		Name string    `gorm:"column:name"`
	}
	var names []nameRow
	q := `SELECT id, name FROM units WHERE organization_id = ? AND id IN ?`
	if by == "agent" {
		q = `SELECT id, full_name AS name FROM users WHERE organization_id = ? AND id IN ?`
	}
	if err := a.DB.Raw(q, orgID, ids).Scan(&names).Error; err != nil {
		return
	}
	byID := map[string]string{}
	for _, n := range names {
		byID[n.ID.String()] = n.Name
	}
	for i := range groups {
		groups[i].Label = byID[groups[i].Key]
	}
}

type usageRowView struct {
	models.MessageUsage
	UnitName  string `gorm:"column:unit_name" json:"unit_name,omitempty"`
	AgentName string `gorm:"column:agent_name" json:"agent_name,omitempty"`
}

// ListWhatsAppUsageMessages returns the ledger rows of the period, newest first.
// They carry identifiers and costs only: never message content or phone numbers.
func (a *App) ListWhatsAppUsageMessages(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceWhatsAppUsage, models.ActionRead)
	if err != nil {
		return nil
	}
	f, _, msg := a.parseUsageFilter(r, orgID)
	if msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}
	where, args := f.where(orgID)
	if ls := string(r.RequestCtx.QueryArgs().Peek("link_state")); ls == "unlinked" || ls == "linked" {
		where += " AND message_usage.link_state = ?"
		args = append(args, ls)
	}
	pg := parsePagination(r)

	var total int64
	if err := a.DB.Raw(`SELECT count(*) FROM message_usage WHERE `+where, args...).Scan(&total).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load the consumption", nil, "")
	}
	rows := []usageRowView{} // never null in the JSON
	if err := a.DB.Raw(`
		SELECT message_usage.*, units.name AS unit_name, users.full_name AS agent_name
		FROM message_usage
		LEFT JOIN units ON units.id = message_usage.unit_id AND units.organization_id = message_usage.organization_id
		LEFT JOIN users ON users.id = message_usage.actor_user_id AND users.organization_id = message_usage.organization_id
		WHERE `+where+` ORDER BY message_usage.sent_at DESC, message_usage.id LIMIT ? OFFSET ?`,
		append(args, pg.Limit, pg.Offset)...).Scan(&rows).Error; err != nil {
		a.Log.Error("Failed to list WhatsApp usage", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load the consumption", nil, "")
	}
	return r.SendEnvelope(listEnvelope("messages", rows, total, pg))
}

// RepriceWhatsAppUsage settles again the rows waiting on a price (`no_rate`,
// `awaiting_pricing`, `unconfirmed`), so a price registered afterwards reaches them.
func (a *App) RepriceWhatsAppUsage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceWhatsAppUsage, models.ActionWrite)
	if err != nil {
		return nil
	}
	if !a.Usage.Enabled() {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Usage recording is turned off (usage.record_enabled = false)", nil, "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := a.Usage.Reprice(ctx, orgID)
	if err != nil {
		a.Log.Error("Failed to reprice WhatsApp usage", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to reprice", nil, "")
	}
	a.logAudit(orgID, userID, "whatsapp_usage", orgID, models.AuditActionUpdated, nil, map[string]any{"reprice": res})
	return r.SendEnvelope(res)
}
