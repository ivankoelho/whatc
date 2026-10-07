package handlers

import (
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// The WhatsApp price table: versioned by validity, never in the code. A row that
// a message_usage already references is immutable except for closing its period
// (valid_to) and notes: changing a price means registering a new validity.

var (
	rateCountryRe  = regexp.MustCompile(`^(\*|[0-9]{1,4})$`)
	rateCategoryRe = regexp.MustCompile(`^[a-z][a-z_]{1,39}$`)
	rateCurrencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
)

// WhatsAppRateRequest is the create/update body of a price.
type WhatsAppRateRequest struct {
	Country   string   `json:"country"`
	Category  string   `json:"category"`
	Price     *float64 `json:"price"`
	Currency  string   `json:"currency"`
	ValidFrom string   `json:"valid_from"` // YYYY-MM-DD
	ValidTo   string   `json:"valid_to"`   // YYYY-MM-DD or empty = open
	Notes     string   `json:"notes"`
}

type whatsAppRateView struct {
	models.WhatsAppRate
	InUse bool `json:"in_use"`
}

// parsedRate is a validated request.
type parsedRate struct {
	Country, Category, Currency, Notes string
	Price                              float64
	From                               time.Time
	To                                 *time.Time
}

func parseRateRequest(req WhatsAppRateRequest) (parsedRate, string) {
	p := parsedRate{
		Country: strings.TrimSpace(req.Country), Category: strings.ToLower(strings.TrimSpace(req.Category)),
		Currency: strings.ToUpper(strings.TrimSpace(req.Currency)), Notes: strings.TrimSpace(req.Notes),
	}
	if !rateCountryRe.MatchString(p.Country) {
		return p, "country must be a calling code (e.g. 55) or *"
	}
	if !rateCategoryRe.MatchString(p.Category) {
		return p, "category must be lowercase letters and underscores (e.g. utility)"
	}
	if !rateCurrencyRe.MatchString(p.Currency) {
		return p, "currency must be an ISO 4217 code (e.g. BRL)"
	}
	if req.Price == nil || *req.Price < 0 {
		return p, "price must be zero or more"
	}
	p.Price = *req.Price
	from, err := time.Parse("2006-01-02", req.ValidFrom)
	if err != nil {
		return p, "valid_from must be a date (YYYY-MM-DD)"
	}
	p.From = from
	if req.ValidTo != "" {
		to, err := time.Parse("2006-01-02", req.ValidTo)
		if err != nil {
			return p, "valid_to must be a date (YYYY-MM-DD)"
		}
		if to.Before(from) {
			return p, "valid_to cannot be before valid_from"
		}
		p.To = &to
	}
	return p, ""
}

// rateOverlaps reports whether another price of the same country and category
// has a period that intersects [from, to] (to == nil means open).
func (a *App) rateOverlaps(orgID uuid.UUID, p parsedRate, excludeID uuid.UUID) (bool, error) {
	q := a.DB.Model(&models.WhatsAppRate{}).
		Where("organization_id = ? AND country = ? AND category = ? AND id <> ?", orgID, p.Country, p.Category, excludeID).
		Where("(valid_to IS NULL OR valid_to >= ?)", p.From)
	if p.To != nil {
		q = q.Where("valid_from <= ?", *p.To)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// rateUsage says whether a message_usage references the price, and the last day a
// message priced with it was sent.
func (a *App) rateUsage(orgID, rateID uuid.UUID) (used bool, lastSent time.Time, err error) {
	var last *time.Time
	row := a.DB.Model(&models.MessageUsage{}).Where("organization_id = ? AND rate_id = ?", orgID, rateID).
		Select("MAX(sent_at)").Row()
	if err = row.Scan(&last); err != nil {
		return false, time.Time{}, err
	}
	if last == nil {
		return false, time.Time{}, nil
	}
	return true, *last, nil
}

// ListWhatsAppRates returns the organization's price table.
func (a *App) ListWhatsAppRates(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceWhatsAppUsage, models.ActionRead)
	if err != nil {
		return nil
	}
	rates := []whatsAppRateView{} // never null in the JSON
	if err := a.DB.Raw(`
		SELECT r.*, EXISTS (SELECT 1 FROM message_usage u WHERE u.rate_id = r.id) AS in_use
		FROM whatsapp_rates r
		WHERE r.organization_id = ?
		ORDER BY r.category, r.country, r.valid_from DESC`, orgID).Scan(&rates).Error; err != nil {
		a.Log.Error("Failed to load WhatsApp rates", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load the price table", nil, "")
	}
	return r.SendEnvelope(map[string]any{"rates": rates})
}

// CreateWhatsAppRate registers a price for a validity period.
func (a *App) CreateWhatsAppRate(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceWhatsAppUsage, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req WhatsAppRateRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	p, msg := parseRateRequest(req)
	if msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}
	overlap, err := a.rateOverlaps(orgID, p, uuid.Nil)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save the price", nil, "")
	}
	if overlap {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Another price for this country and category overlaps this period", nil, "")
	}
	rate := models.WhatsAppRate{
		OrganizationID: orgID, Country: p.Country, Category: p.Category, Price: p.Price, Currency: p.Currency,
		ValidFrom: p.From, ValidTo: p.To, Notes: p.Notes, CreatedBy: &userID,
	}
	if err := a.DB.Create(&rate).Error; err != nil {
		if isUniqueViolation(err) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "A price with the same country, category and start date already exists", nil, "")
		}
		a.Log.Error("Failed to create WhatsApp rate", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save the price", nil, "")
	}
	a.logAudit(orgID, userID, "whatsapp_rate", rate.ID, models.AuditActionCreated, nil, rate)
	return r.SendEnvelope(whatsAppRateView{WhatsAppRate: rate})
}

// UpdateWhatsAppRate edits a price. A price already used by a message only accepts
// closing an open period (valid_to, on or after the last message it priced) and
// notes; anything else must be a new validity.
func (a *App) UpdateWhatsAppRate(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceWhatsAppUsage, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "price")
	if err != nil {
		return nil
	}
	rate, err := findByIDAndOrg[models.WhatsAppRate](a.DB, r, id, orgID, "Price")
	if err != nil {
		return nil
	}
	var req WhatsAppRateRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	p, msg := parseRateRequest(req)
	if msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}
	used, lastSent, err := a.rateUsage(orgID, id)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save the price", nil, "")
	}
	if used {
		sameCore := rate.Country == p.Country && rate.Category == p.Category && rate.Currency == p.Currency &&
			rate.Price == p.Price && rate.ValidFrom.Equal(p.From)
		closes := rate.ValidTo == nil && p.To != nil
		keeps := (rate.ValidTo == nil && p.To == nil) || (rate.ValidTo != nil && p.To != nil && rate.ValidTo.Equal(*p.To))
		if !sameCore || (!closes && !keeps) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict,
				"This price was already used by messages: register a new validity instead of changing it", nil, "")
		}
		if closes && p.To.Before(time.Date(lastSent.Year(), lastSent.Month(), lastSent.Day(), 0, 0, 0, 0, time.UTC)) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict,
				"valid_to cannot be before the last message priced with this price", nil, "")
		}
	}
	overlap, err := a.rateOverlaps(orgID, p, id)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save the price", nil, "")
	}
	if overlap {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Another price for this country and category overlaps this period", nil, "")
	}
	old := *rate
	rate.Country, rate.Category, rate.Price, rate.Currency = p.Country, p.Category, p.Price, p.Currency
	rate.ValidFrom, rate.ValidTo, rate.Notes = p.From, p.To, p.Notes
	if err := a.DB.Save(rate).Error; err != nil {
		if isUniqueViolation(err) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "A price with the same country, category and start date already exists", nil, "")
		}
		a.Log.Error("Failed to update WhatsApp rate", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save the price", nil, "")
	}
	a.logAudit(orgID, userID, "whatsapp_rate", rate.ID, models.AuditActionUpdated, old, *rate)
	return r.SendEnvelope(whatsAppRateView{WhatsAppRate: *rate, InUse: used})
}

// DeleteWhatsAppRate removes a price that no message used.
func (a *App) DeleteWhatsAppRate(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceWhatsAppUsage, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "price")
	if err != nil {
		return nil
	}
	rate, err := findByIDAndOrg[models.WhatsAppRate](a.DB, r, id, orgID, "Price")
	if err != nil {
		return nil
	}
	used, _, err := a.rateUsage(orgID, id)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete the price", nil, "")
	}
	if used {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "This price was already used by messages and cannot be deleted", nil, "")
	}
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).Delete(&models.WhatsAppRate{}).Error; err != nil {
		a.Log.Error("Failed to delete WhatsApp rate", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete the price", nil, "")
	}
	a.logAudit(orgID, userID, "whatsapp_rate", id, models.AuditActionDeleted, *rate, nil)
	return r.SendEnvelope(map[string]any{"deleted": true})
}
