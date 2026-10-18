package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// GetSalesOpportunitiesSummary returns the unified sales screen's indicators,
// scoped like the list (own / team / all) and narrowed by the same seller and
// store filters. Open-pipeline numbers are "right now"; with ?from&to
// (YYYY-MM-DD) the converted/lost numbers count closings inside that period,
// otherwise all time.
func (a *App) GetSalesOpportunitiesSummary(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionRead)
	if err != nil {
		return nil
	}

	base := func() *gorm.DB {
		q := a.DB.Model(&models.SalesOpportunity{}).Where("sales_opportunities.organization_id = ?", orgID)
		return filterSalesOpportunities(a.visibleSalesOpportunities(q, userID, orgID), a.DB, r)
	}

	fromStr := string(r.RequestCtx.QueryArgs().Peek("from"))
	toStr := string(r.RequestCtx.QueryArgs().Peek("to"))
	closedIn := func(q *gorm.DB, col string) *gorm.DB { return q }
	if fromStr != "" && toStr != "" {
		start, end, errMsg := parseDateRange(fromStr, toStr, a.orgLocation(orgID))
		if errMsg != "" {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errMsg, nil, "")
		}
		closedIn = func(q *gorm.DB, col string) *gorm.DB {
			return q.Where("sales_opportunities."+col+" >= ? AND sales_opportunities."+col+" < ?", start, end)
		}
	}

	type agg struct {
		Count int64
		Value float64
	}
	sum := func(q *gorm.DB) (agg, error) {
		var out agg
		err := q.Select("COUNT(*) AS count, COALESCE(SUM(sales_opportunities.estimated_value), 0) AS value").Scan(&out).Error
		return out, err
	}
	count := func(q *gorm.DB) (int64, error) {
		var n int64
		return n, q.Count(&n).Error
	}

	open, err1 := sum(base().Where("sales_opportunities.status = ?", models.SalesOpportunityStatusAberta))
	converted, err2 := sum(closedIn(base().Where("sales_opportunities.status = ?", models.SalesOpportunityStatusConvertida), "converted_at"))
	lost, err3 := sum(closedIn(base().Where("sales_opportunities.status = ?", models.SalesOpportunityStatusPerdida), "lost_at"))
	total, err4 := count(base())
	potencial, err5 := count(base().Where("sales_opportunities.status = ? AND sales_opportunities.stage = ?",
		models.SalesOpportunityStatusAberta, models.SalesOpportunityStagePotencial))
	semDirecionamento, err6 := count(base().Where("sales_opportunities.status = ? AND sales_opportunities.direcionamento IS NULL",
		models.SalesOpportunityStatusAberta))
	slaBreached, err7 := count(base().Where("sales_opportunities.status = ? AND sales_opportunities.sla_breached = ?",
		models.SalesOpportunityStatusAberta, true))
	for _, e := range []error{err1, err2, err3, err4, err5, err6, err7} {
		if e != nil {
			a.Log.Error("Failed to summarize sales opportunities", "error", e)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to summarize sales opportunities", nil, "")
		}
	}

	// Same definition everywhere (spec §8): convertidas / (convertidas + perdidas).
	conversionRate := 0.0
	if closed := converted.Count + lost.Count; closed > 0 {
		conversionRate = float64(converted.Count) / float64(closed) * 100
	}

	return r.SendEnvelope(map[string]any{
		"total":              total,
		"abertas":            open.Count,
		"potencial":          potencial,
		"sem_direcionamento": semDirecionamento,
		"sla_breached":       slaBreached,
		"convertida":         converted.Count,
		"perdida":            lost.Count,
		"conversion_rate":    conversionRate,
		"valor_prospecto":    open.Value,
		"valor_convertido":   converted.Value,
		"valor_perdido":      lost.Value,
	})
}

// GetSalesOpportunitiesScope tells the sales screen what the caller may see
// and which sellers/stores its manual filters can offer: everyone with
// view_all, the caller's team-mates with view_team, nobody (own only) for a
// seller.
func (a *App) GetSalesOpportunitiesScope(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionRead)
	if err != nil {
		return nil
	}

	scope := a.salesScope(userID, orgID)
	type option struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}
	sellers := []option{}
	units := []option{}

	if scope != salesScopeOwn {
		members := a.DB.Model(&models.UserOrganization{}).Select("user_id").Where("organization_id = ?", orgID)
		sq := a.DB.Model(&models.User{}).Select("users.id, users.full_name AS name").
			Where("users.organization_id = ? OR users.id IN (?)", orgID, members).
			Where("users.is_active = ?", true)
		uq := a.DB.Model(&models.Unit{}).Select("units.id, units.name").Where("units.organization_id = ?", orgID)
		if scope == salesScopeTeam {
			sq = sq.Where("users.id IN (?)", a.salesTeamMates(userID))
			uq = uq.Where("units.id IN (?)", a.DB.Model(&models.Team{}).Select("teams.unit_id").
				Where("teams.unit_id IS NOT NULL AND teams.id IN (?)",
					a.DB.Model(&models.TeamMember{}).Select("team_id").Where("user_id = ?", userID)))
		}
		if err := sq.Order("users.full_name").Scan(&sellers).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load sellers", nil, "")
		}
		if err := uq.Order("units.name").Scan(&units).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load stores", nil, "")
		}
	}

	return r.SendEnvelope(map[string]any{"scope": scope, "sellers": sellers, "units": units})
}
