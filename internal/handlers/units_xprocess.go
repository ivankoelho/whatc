package handlers

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/contactutil"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/xprocess"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// Mapping of a local Unit to an X2 store (cod_empresa).
//
// It is ADMINISTRATIVE and explicit: an administrator (units:write) picks the X2
// store for each unit. Nothing is created, changed or removed because of what the
// X2 list says, names are never matched silently (the suggestion below is only a
// hint shown next to the list) and, once saved, the code is read locally: X2 is
// consulted only while an administrator changes the mapping, so an X2 outage or its
// D-1 snapshot never breaks a mapping that is already saved.

// xprocessLojaView is one X2 store as the units screen sees it.
type xprocessLojaView struct {
	CodEmpresa         string     `json:"cod_empresa"`
	RazaoSocialEmpresa string     `json:"razao_social_empresa"`
	CNPJEmpresa        string     `json:"cnpj_empresa"`
	UnitID             *uuid.UUID `json:"unit_id,omitempty"`   // the unit already linked to this store
	UnitName           string     `json:"unit_name,omitempty"` //
	// Suggested* is a presentation hint ("this looks like that unit"), NOT state:
	// it is never saved by itself.
	SuggestedUnitID   *uuid.UUID `json:"suggested_unit_id,omitempty"`
	SuggestedUnitName string     `json:"suggested_unit_name,omitempty"`
}

// fetchXProcessLojas reads the store list with the organization's own X2
// credential. It returns the HTTP status and message to send when it cannot.
func (a *App) fetchXProcessLojas(orgID uuid.UUID) ([]xprocess.Loja, int, string) {
	var integ models.XProcessIntegration
	if err := a.DB.Where("organization_id = ? AND is_active = ?", orgID, true).First(&integ).Error; err != nil {
		return nil, fasthttp.StatusConflict, "The X2 integration is not configured or is inactive"
	}
	integ.DecryptSecrets(a.Config.App.EncryptionKey)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	lojas, err := xprocess.New(a.Log, integ.BaseURL).ListarLojas(ctx, integ.APIKey)
	if err != nil {
		a.Log.Warn("units: failed to load X2 stores", "org_id", orgID, "error", err)
		return nil, fasthttp.StatusBadGateway, "Could not load the stores from X2"
	}
	return lojas, 0, ""
}

// ListUnitXProcessLojas lists the X2 stores next to the units already linked to
// them and a name-based suggestion for the rest.
//
//	GET /api/units/xprocess-lojas   (units:write)
func (a *App) ListUnitXProcessLojas(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceUnits, models.ActionWrite)
	if err != nil {
		return nil
	}
	lojas, status, msg := a.fetchXProcessLojas(orgID)
	if status != 0 {
		return r.SendErrorEnvelope(status, msg, nil, "")
	}

	var units []models.Unit
	if err := a.DB.Where("organization_id = ?", orgID).Find(&units).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load units", nil, "")
	}
	byCode := map[string]*models.Unit{}
	for i := range units {
		if units[i].XProcessCodEmpresa != nil {
			byCode[*units[i].XProcessCodEmpresa] = &units[i]
		}
	}
	suggestions := suggestUnitsForLojas(lojas, units)

	sort.SliceStable(lojas, func(i, j int) bool {
		a, errA := strconv.Atoi(lojas[i].CodEmpresa)
		b, errB := strconv.Atoi(lojas[j].CodEmpresa)
		if errA == nil && errB == nil {
			return a < b
		}
		return lojas[i].CodEmpresa < lojas[j].CodEmpresa
	})
	out := make([]xprocessLojaView, 0, len(lojas))
	for _, l := range lojas {
		v := xprocessLojaView{CodEmpresa: l.CodEmpresa, RazaoSocialEmpresa: strings.Join(strings.Fields(l.RazaoSocialEmpresa), " "), CNPJEmpresa: l.CNPJEmpresa}
		if u, ok := byCode[l.CodEmpresa]; ok {
			v.UnitID, v.UnitName = &u.ID, u.Name
		} else if u, ok := suggestions[l.CodEmpresa]; ok {
			v.SuggestedUnitID, v.SuggestedUnitName = &u.ID, u.Name
		}
		out = append(out, v)
	}
	return r.SendEnvelope(map[string]any{"lojas": out})
}

type setUnitXProcessLojaRequest struct {
	// CodEmpresa is the X2 store; "" removes the link (no call to X2).
	CodEmpresa string `json:"cod_empresa"`
	// FillCNPJ asks to copy the store's CNPJ into the unit. Off unless the
	// administrator ticks it; it only fills an EMPTY CNPJ and never fails the link.
	FillCNPJ bool `json:"fill_cnpj"`
}

// SetUnitXProcessLoja links a unit to an X2 store, changes the store, or clears it.
//
//	PUT /api/units/{id}/xprocess-loja   (units:write)
func (a *App) SetUnitXProcessLoja(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceUnits, models.ActionWrite)
	if err != nil {
		return nil
	}
	unitID, err := parsePathUUID(r, "id", "unit")
	if err != nil {
		return nil
	}
	unit, err := findByIDAndOrg[models.Unit](a.DB, r, unitID, orgID, "Unit")
	if err != nil {
		return nil
	}
	var req setUnitXProcessLojaRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	cod := strings.TrimSpace(req.CodEmpresa)
	// Snapshot for the audit trail. The code is a pointer that GORM updates in place,
	// so copying the struct alone would make "before" already show the new value.
	before := *unit
	if unit.XProcessCodEmpresa != nil {
		old := *unit.XProcessCodEmpresa
		before.XProcessCodEmpresa = &old
	}

	if cod == "" {
		// Clearing is local only: it never needs X2.
		if err := a.DB.Model(unit).Update("xprocess_cod_empresa", nil).Error; err != nil {
			a.Log.Error("Failed to clear unit X2 store", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update unit", nil, "")
		}
		return a.finishUnitXProcessLoja(r, orgID, userID, &before, unitID, false, "")
	}

	// Linking is validated against X2 NOW (a typo or a store that does not exist is
	// refused) and never again on reads.
	lojas, status, msg := a.fetchXProcessLojas(orgID)
	if status != 0 {
		return r.SendErrorEnvelope(status, msg, nil, "")
	}
	var loja *xprocess.Loja
	for i := range lojas {
		if lojas[i].CodEmpresa == cod {
			loja = &lojas[i]
			break
		}
	}
	if loja == nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "cod_empresa does not exist in X2", nil, "")
	}

	var taken int64
	a.DB.Model(&models.Unit{}).
		Where("organization_id = ? AND xprocess_cod_empresa = ? AND id <> ?", orgID, cod, unitID).
		Count(&taken)
	if taken > 0 {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Another unit is already linked to this X2 store", nil, "")
	}
	if err := a.DB.Model(unit).Update("xprocess_cod_empresa", cod).Error; err != nil {
		if isUniqueXProcessLojaViolation(err) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "Another unit is already linked to this X2 store", nil, "")
		}
		a.Log.Error("Failed to link unit to X2 store", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update unit", nil, "")
	}

	cnpjFilled, cnpjNote := false, ""
	if req.FillCNPJ {
		cnpjFilled, cnpjNote = a.fillUnitCNPJFromLoja(unit, loja)
	}
	return a.finishUnitXProcessLoja(r, orgID, userID, &before, unitID, cnpjFilled, cnpjNote)
}

// fillUnitCNPJFromLoja copies the store's CNPJ into a unit whose CNPJ is EMPTY.
// It never replaces an existing CNPJ and never fails the link: when it does not
// fill, it says why.
func (a *App) fillUnitCNPJFromLoja(unit *models.Unit, loja *xprocess.Loja) (bool, string) {
	if unit.CNPJ != "" {
		return false, "unit_has_cnpj"
	}
	digits := contactutil.NormalizePhone(loja.CNPJEmpresa)
	if !contactutil.ValidCNPJ(digits) {
		return false, "store_cnpj_invalid"
	}
	if err := a.DB.Model(unit).Update("cnpj", digits).Error; err != nil {
		if isUniqueCNPJViolation(err) {
			return false, "cnpj_in_use"
		}
		a.Log.Error("Failed to fill unit CNPJ from X2 store", "error", err)
		return false, "cnpj_update_failed"
	}
	return true, ""
}

func (a *App) finishUnitXProcessLoja(r *fastglue.Request, orgID, userID uuid.UUID, before *models.Unit, unitID uuid.UUID, cnpjFilled bool, cnpjNote string) error {
	var after models.Unit
	if err := a.DB.First(&after, "id = ?", unitID).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load unit", nil, "")
	}
	// The audit comparison walks the keys of the NEW state, and a removed link has no
	// key at all (omitempty), so a removal would leave no trace: record it explicitly.
	var extra []map[string]any
	if before.XProcessCodEmpresa != nil && after.XProcessCodEmpresa == nil {
		extra = append(extra, map[string]any{"field": "xprocess_cod_empresa", "old_value": *before.XProcessCodEmpresa, "new_value": nil})
	}
	a.logAudit(orgID, userID, "unit", after.ID, models.AuditActionUpdated, before, &after, extra...)
	return r.SendEnvelope(map[string]any{"unit": after, "cnpj_filled": cnpjFilled, "cnpj_note": cnpjNote})
}

func isUniqueXProcessLojaViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_units_org_xprocess_loja"
}

// unitsByXProcessCode resolves X2 store codes to the units linked to them.
func (a *App) unitsByXProcessCode(orgID uuid.UUID, codes ...string) map[string]models.Unit {
	out := map[string]models.Unit{}
	var cleaned []string
	for _, c := range codes {
		if c != "" {
			cleaned = append(cleaned, c)
		}
	}
	if len(cleaned) == 0 {
		return out
	}
	var units []models.Unit
	if err := a.DB.Where("organization_id = ? AND xprocess_cod_empresa IN ?", orgID, cleaned).Find(&units).Error; err != nil {
		return out
	}
	for _, u := range units {
		if u.XProcessCodEmpresa != nil {
			out[*u.XProcessCodEmpresa] = u
		}
	}
	return out
}

// --- name suggestion (presentation hint only) ---

var parenthetical = regexp.MustCompile(`\(([^)]+)\)`)

var accentFolder = strings.NewReplacer(
	"Á", "A", "À", "A", "Â", "A", "Ã", "A", "Ä", "A", "É", "E", "È", "E", "Ê", "E", "Ë", "E",
	"Í", "I", "Ì", "I", "Î", "I", "Ï", "I", "Ó", "O", "Ò", "O", "Ô", "O", "Õ", "O", "Ö", "O",
	"Ú", "U", "Ù", "U", "Û", "U", "Ü", "U", "Ç", "C", "Ñ", "N",
)

func foldName(s string) []string {
	s = accentFolder.Replace(strings.ToUpper(s))
	return strings.Fields(s)
}

// lojaName is the store's own name: what X2 puts in parentheses at the end of the
// company name ("ATACADAO DOS PISOS LTDA  (PORTO)" -> PORTO). Without parentheses
// the company name itself is returned, which rarely matches a unit.
func lojaName(razao string) []string {
	if m := parenthetical.FindAllStringSubmatch(razao, -1); len(m) > 0 {
		return foldName(m[len(m)-1][1])
	}
	return foldName(razao)
}

func wordsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// wordPrefix reports whether one of the two word lists is a prefix of the other
// (PORTO / PORTO SEGURO).
func wordPrefix(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	return wordsEqual(short, long[:len(short)])
}

// suggestUnitsForLojas proposes, for stores not yet linked, the unit whose name
// matches. Two tiers, exact name first and then word-prefix; a pair is suggested
// only when the match is UNIQUE in both directions (one store for that unit and
// one unit for that store), so an ambiguous name ("CAMACARI" vs "CAMACARI 2")
// yields no suggestion instead of a wrong one. Units already linked are skipped.
func suggestUnitsForLojas(lojas []xprocess.Loja, units []models.Unit) map[string]*models.Unit {
	linked := map[string]bool{}
	for _, u := range units {
		if u.XProcessCodEmpresa != nil {
			linked[*u.XProcessCodEmpresa] = true
		}
	}
	freeLojas := map[string][]string{}
	for _, l := range lojas {
		if !linked[l.CodEmpresa] {
			freeLojas[l.CodEmpresa] = lojaName(l.RazaoSocialEmpresa)
		}
	}
	freeUnits := map[uuid.UUID]*models.Unit{}
	for i := range units {
		if units[i].XProcessCodEmpresa == nil {
			freeUnits[units[i].ID] = &units[i]
		}
	}

	out := map[string]*models.Unit{}
	for _, match := range []func(a, b []string) bool{wordsEqual, wordPrefix} {
		lojaToUnits := map[string][]uuid.UUID{}
		unitToLojas := map[uuid.UUID][]string{}
		for cod, ln := range freeLojas {
			for id, u := range freeUnits {
				if match(ln, foldName(u.Name)) {
					lojaToUnits[cod] = append(lojaToUnits[cod], id)
					unitToLojas[id] = append(unitToLojas[id], cod)
				}
			}
		}
		for cod, ids := range lojaToUnits {
			if len(ids) == 1 && len(unitToLojas[ids[0]]) == 1 {
				out[cod] = freeUnits[ids[0]]
				delete(freeLojas, cod)
				delete(freeUnits, ids[0])
			}
		}
	}
	return out
}
