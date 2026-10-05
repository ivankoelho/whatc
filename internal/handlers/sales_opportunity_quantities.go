package handlers

import (
	"math"

	"github.com/shridarpatil/whatomate/internal/models"
)

// Limits of sales_opportunities.estimated_quantity / realized_quantity
// (numeric(14,3)): 11 integer digits, 3 decimals.
const maxSalesQuantity = 99999999999.999

// maxSalesMoney is a sanity ceiling for estimated/realized value (numeric has no
// scale limit; this only stops absurd or non-finite input).
const maxSalesMoney = 1e13

// validSalesQuantity accepts a finite, non-negative quantity that fits
// numeric(14,3) and has at most 3 decimals. Finer input is rejected instead of
// being silently rounded by the database.
func validSalesQuantity(q float64) bool {
	if math.IsNaN(q) || math.IsInf(q, 0) || q < 0 || q > maxSalesQuantity {
		return false
	}
	return math.Abs(q*1000-math.Round(q*1000)) < 1e-6
}

func validSalesMoney(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= maxSalesMoney
}

// parseSalesUnit reads an optional unit_of_measure from a request: nil means
// "not sent", "" means "clear it" (set, nil unit), anything else must be in the
// closed list (ok=false otherwise).
func parseSalesUnit(raw *string) (set bool, unit *models.SalesUnitOfMeasure, ok bool) {
	if raw == nil {
		return false, nil, true
	}
	if *raw == "" {
		return true, nil, true
	}
	u := models.SalesUnitOfMeasure(*raw)
	if !u.IsValid() {
		return false, nil, false
	}
	return true, &u, true
}
