package handlers

import (
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/contactutil"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/xprocess"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// Import of X2 stores as Units.
//
// It is the counterpart of the link (units_xprocess.go): the link points an
// EXISTING unit at a store, the import CREATES the unit for a store that has none.
// It is administrative and explicit (units:write, the stores the administrator
// ticked), idempotent, and never resolves a clash on its own:
//
//   - identity is organization + xprocess_cod_empresa (unique index
//     idx_units_org_xprocess_loja); names and CNPJ never identify a store;
//   - a store whose code is already on a unit is NOT created again. Re-importing
//     it changes nothing, except that an EMPTY local CNPJ is filled from X2 (the
//     same rule as fill_cnpj on the link). name, type, active and every other
//     local field are never overwritten;
//   - a store whose CNPJ or name belongs to a unit with no link to it is reported
//     as a conflict (with that unit) and nothing is created.

const maxXProcessImport = 200

// Import statuses (list: import_status; import result: status).
const (
	importNew            = "new"
	importAlreadyImport  = "already_imported"
	importConflict       = "conflict"
	importResCreated     = "created"
	importResUpdated     = "updated"
	importResExists      = "already_exists"
	importResConflict    = "conflict"
	importResFailed      = "failed"
	reasonCNPJInUse      = "cnpj_in_use"
	reasonNameInUse      = "name_in_use"
	reasonNotInX2        = "not_in_x2"
	reasonCreateFailed   = "create_failed"
	noteStoreCNPJInvalid = "store_cnpj_invalid"
	noteCNPJDiffers      = "cnpj_differs"
)

// unitNameFromLoja is the name a unit gets: the store name exactly as X2 has it
// ("ATACADAO DOS PISOS LTDA  (PORTO)" stays that way). It is never shortened or
// renamed; only surrounding whitespace is dropped.
func unitNameFromLoja(razao string) string {
	return strings.TrimSpace(razao)
}

// lojaCNPJ is the store CNPJ as digits, "" when it is not a valid CNPJ.
func lojaCNPJ(l xprocess.Loja) string {
	d := contactutil.NormalizePhone(l.CNPJEmpresa)
	if !contactutil.ValidCNPJ(d) {
		return ""
	}
	return d
}

type importClass struct {
	Status string // importNew | importAlreadyImport | importConflict
	Reason string // for a conflict: reasonCNPJInUse | reasonNameInUse
	Unit   *models.Unit
}

// classifyLoja is the ONE place that decides what importing a store would do, for
// the listing and for the import itself, so the screen and the server never disagree.
func classifyLoja(l xprocess.Loja, units []models.Unit) importClass {
	for i := range units {
		if units[i].XProcessCodEmpresa != nil && *units[i].XProcessCodEmpresa == l.CodEmpresa {
			return importClass{Status: importAlreadyImport, Unit: &units[i]}
		}
	}
	if cnpj := lojaCNPJ(l); cnpj != "" {
		for i := range units {
			if units[i].CNPJ == cnpj {
				return importClass{Status: importConflict, Reason: reasonCNPJInUse, Unit: &units[i]}
			}
		}
	}
	name := unitNameFromLoja(l.RazaoSocialEmpresa)
	for i := range units {
		if strings.EqualFold(units[i].Name, name) {
			return importClass{Status: importConflict, Reason: reasonNameInUse, Unit: &units[i]}
		}
	}
	return importClass{Status: importNew}
}

type xprocessImportRequest struct {
	CodEmpresa []string `json:"cod_empresa"`
}

type xprocessImportResult struct {
	CodEmpresa   string             `json:"cod_empresa"`
	Status       string             `json:"status"` // created | updated | already_exists | conflict | failed
	Reason       string             `json:"reason,omitempty"`
	Notes        []string           `json:"notes,omitempty"`
	X2Store      *xprocessLojaStore `json:"x2_store,omitempty"`
	Unit         *models.Unit       `json:"unit,omitempty"`          // created or already there
	ExistingUnit *xprocessUnitBrief `json:"existing_unit,omitempty"` // the other unit of a conflict
}

type xprocessLojaStore struct {
	CodEmpresa         string `json:"cod_empresa"`
	RazaoSocialEmpresa string `json:"razao_social_empresa"`
	CNPJEmpresa        string `json:"cnpj_empresa"`
}

type xprocessUnitBrief struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	CNPJ string    `json:"cnpj,omitempty"`
}

// ImportUnitsFromXProcess creates, for each requested X2 store, the unit that
// stands for it. One store never blocks the others: each is saved on its own and
// reported in results.
//
//	POST /api/units/xprocess-import   {"cod_empresa": ["002", "003"]}   (units:write)
func (a *App) ImportUnitsFromXProcess(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceUnits, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req xprocessImportRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	seen := map[string]bool{}
	var codes []string
	for _, c := range req.CodEmpresa {
		if c = strings.TrimSpace(c); c != "" && !seen[c] {
			seen[c] = true
			codes = append(codes, c)
		}
	}
	if len(codes) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "cod_empresa is required", nil, "")
	}
	if len(codes) > maxXProcessImport {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Too many stores in one import", nil, "")
	}

	// Nothing is written when X2 cannot be read.
	lojas, status, msg := a.fetchXProcessLojas(orgID)
	if status != 0 {
		return r.SendErrorEnvelope(status, msg, nil, "")
	}
	byCode := map[string]xprocess.Loja{}
	for _, l := range lojas {
		byCode[l.CodEmpresa] = l
	}

	var units []models.Unit
	if err := a.DB.Where("organization_id = ?", orgID).Find(&units).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load units", nil, "")
	}

	results := make([]xprocessImportResult, 0, len(codes))
	summary := map[string]int{"selected": len(codes), "created": 0, "updated": 0, "already_exists": 0, "conflicts": 0, "failed": 0}
	for _, cod := range codes {
		res := a.importOneLoja(orgID, userID, cod, byCode, &units)
		switch res.Status {
		case importResCreated:
			summary["created"]++
		case importResUpdated:
			summary["updated"]++
		case importResExists:
			summary["already_exists"]++
		case importResConflict:
			summary["conflicts"]++
		default:
			summary["failed"]++
		}
		results = append(results, res)
	}
	return r.SendEnvelope(map[string]any{"summary": summary, "results": results})
}

// importOneLoja handles one store. units is the organization's current list and is
// kept up to date so two stores of the same batch cannot clash unseen.
func (a *App) importOneLoja(orgID, userID uuid.UUID, cod string, byCode map[string]xprocess.Loja, units *[]models.Unit) xprocessImportResult {
	loja, ok := byCode[cod]
	if !ok {
		return xprocessImportResult{CodEmpresa: cod, Status: importResFailed, Reason: reasonNotInX2}
	}
	res := xprocessImportResult{
		CodEmpresa: cod,
		X2Store:    &xprocessLojaStore{CodEmpresa: loja.CodEmpresa, RazaoSocialEmpresa: strings.Join(strings.Fields(loja.RazaoSocialEmpresa), " "), CNPJEmpresa: loja.CNPJEmpresa},
	}
	cnpj := lojaCNPJ(loja)

	class := classifyLoja(loja, *units)
	switch class.Status {
	case importConflict:
		res.Status, res.Reason = importResConflict, class.Reason
		res.ExistingUnit = &xprocessUnitBrief{ID: class.Unit.ID, Name: class.Unit.Name, CNPJ: class.Unit.CNPJ}
		return res

	case importAlreadyImport:
		unit := class.Unit
		res.Status, res.Unit = importResExists, unit
		switch {
		case cnpj == "":
			// nothing to copy
		case unit.CNPJ == "":
			// Only an EMPTY local CNPJ is filled, and only if no other unit has it.
			if holder := unitWithCNPJ(*units, cnpj, unit.ID); holder != nil {
				res.Notes = append(res.Notes, reasonCNPJInUse)
				break
			}
			before := *unit
			if err := a.DB.Model(unit).Update("cnpj", cnpj).Error; err != nil {
				a.Log.Error("Failed to fill unit CNPJ from X2 store", "error", err)
				res.Notes = append(res.Notes, reasonCNPJInUse)
				break
			}
			unit.CNPJ = cnpj
			a.logAudit(orgID, userID, "unit", unit.ID, models.AuditActionUpdated, &before, unit,
				map[string]any{"field": "source", "old_value": nil, "new_value": "xprocess_import"})
			res.Status = importResUpdated
		case unit.CNPJ != cnpj:
			res.Notes = append(res.Notes, noteCNPJDiffers)
		}
		return res
	}

	// New store: create its unit. active=true; type and the rest stay empty.
	unit := models.Unit{
		OrganizationID:     orgID,
		Name:               unitNameFromLoja(loja.RazaoSocialEmpresa),
		CNPJ:               cnpj,
		Active:             true,
		XProcessCodEmpresa: &cod,
	}
	if cnpj == "" {
		res.Notes = append(res.Notes, noteStoreCNPJInvalid)
	}
	if err := a.DB.Create(&unit).Error; err != nil {
		// A concurrent import or a hand-made unit won the race: report it, never duplicate.
		switch {
		case isUniqueXProcessLojaViolation(err):
			res.Status, res.Reason = importResExists, ""
			if got := a.unitsByXProcessCode(orgID, cod); len(got) > 0 {
				u := got[cod]
				res.Unit = &u
			}
		case isUniqueCNPJViolation(err):
			res.Status, res.Reason = importResConflict, reasonCNPJInUse
		case isUniqueNameViolation(err):
			res.Status, res.Reason = importResConflict, reasonNameInUse
		default:
			a.Log.Error("Failed to import X2 store as unit", "org_id", orgID, "cod_empresa", cod, "error", err)
			res.Status, res.Reason = importResFailed, reasonCreateFailed
		}
		return res
	}
	*units = append(*units, unit)
	a.logAudit(orgID, userID, "unit", unit.ID, models.AuditActionCreated, nil, &unit,
		map[string]any{"field": "source", "old_value": nil, "new_value": "xprocess_import"})
	res.Status, res.Unit = importResCreated, &unit
	return res
}

func unitWithCNPJ(units []models.Unit, cnpj string, except uuid.UUID) *models.Unit {
	for i := range units {
		if units[i].CNPJ == cnpj && units[i].ID != except {
			return &units[i]
		}
	}
	return nil
}
