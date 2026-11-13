package handlers

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// Internal tickets ("Chamado interno") are occurrences with Kind = internal: a
// request between departments, with a requester inside the company instead of a
// customer. They share everything else with service protocols: numbering,
// stages, timeline, participants, files, SLA, and the case-scope access rules.

// CreateInternalTicketRequest is the body for opening an internal ticket.
type CreateInternalTicketRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`

	// TicketType: incident (something broke) or request (something is needed).
	TicketType string `json:"ticket_type"`
	// Impact x Urgency give the priority (ITIL matrix). Priority is only read
	// when either is missing.
	Impact   string `json:"impact"`
	Urgency  string `json:"urgency"`
	Priority string `json:"priority"`

	// Destination: at least a team or a department must say who handles it.
	TeamID       *string `json:"team_id"`
	DepartmentID *string `json:"department_id"`
	UnitID       *string `json:"unit_id"`
	CategoryID   *string `json:"category_id"`

	RequesterDepartmentID *string `json:"requester_department_id"`
	AssignedUserID        *string `json:"assigned_user_id"`
	DueDate               string  `json:"due_date"` // "2006-01-02", optional
	AffectedItem          string  `json:"affected_item"`

	// ObserverUserIDs follow the ticket (read-only plus comments).
	ObserverUserIDs []string `json:"observer_user_ids"`
}

var internalLevels = map[string]int{"low": 1, "medium": 2, "high": 3}

// internalPriority is the ITIL priority matrix: impact + urgency (each 1..3).
// 6 urgent, 5 high, 4 normal, 2-3 low.
func internalPriority(impact, urgency string) models.OccurrencePriority {
	switch internalLevels[impact] + internalLevels[urgency] {
	case 6:
		return models.OccurrencePriorityUrgent
	case 5:
		return models.OccurrencePriorityHigh
	case 4:
		return models.OccurrencePriorityNormal
	}
	return models.OccurrencePriorityLow
}

// CreateInternalTicket opens a ticket between departments and issues its number.
func (a *App) CreateInternalTicket(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req CreateInternalTicketRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	bad := func(msg string) error {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}

	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	if req.Title == "" || len(req.Title) > 255 {
		return bad("title is required (max 255 characters)")
	}
	if req.Description == "" {
		return bad("description is required")
	}
	if len(req.AffectedItem) > 255 {
		return bad("affected_item is too long")
	}
	if req.TicketType == "" {
		req.TicketType = "request"
	}
	if req.TicketType != "incident" && req.TicketType != "request" {
		return bad("Invalid ticket_type")
	}
	if (req.Impact != "" && internalLevels[req.Impact] == 0) || (req.Urgency != "" && internalLevels[req.Urgency] == 0) {
		return bad("impact and urgency must be low, medium or high")
	}

	priority := models.OccurrencePriority(req.Priority)
	switch {
	case req.Impact != "" && req.Urgency != "":
		priority = internalPriority(req.Impact, req.Urgency)
	case priority == "":
		priority = models.OccurrencePriorityNormal
	case priority != models.OccurrencePriorityLow && priority != models.OccurrencePriorityNormal &&
		priority != models.OccurrencePriorityHigh && priority != models.OccurrencePriorityUrgent:
		return bad("Invalid priority")
	}

	var dueDate *time.Time
	if req.DueDate != "" {
		d, err := time.ParseInLocation("2006-01-02", req.DueDate, a.orgLocation(orgID))
		if err != nil {
			return bad("due_date must be YYYY-MM-DD")
		}
		dueDate = &d
	}

	// Destination. The team is the queue; unit/department default from it.
	var teamID, unitID, deptID *uuid.UUID
	var teamUnitID, teamDeptID *uuid.UUID
	if req.TeamID != nil && *req.TeamID != "" {
		id, err := uuid.Parse(*req.TeamID)
		if err != nil {
			return bad("Invalid team_id")
		}
		team, err := findByIDAndOrg[models.Team](a.DB, r, id, orgID, "Team")
		if err != nil {
			return nil
		}
		if !team.IsActive {
			return bad("Team is inactive")
		}
		teamID, unitID, deptID = &id, team.UnitID, team.DepartmentID
		teamUnitID, teamDeptID = team.UnitID, team.DepartmentID
	}
	// orgRef parses an optional id and checks it belongs to the organization.
	orgRef := func(raw *string, field string, check func(id uuid.UUID) error) (*uuid.UUID, bool) {
		if raw == nil || *raw == "" {
			return nil, true
		}
		id, perr := uuid.Parse(*raw)
		if perr != nil {
			_ = bad("Invalid " + field)
			return nil, false
		}
		if err := check(id); err != nil {
			return nil, false // findByIDAndOrg already answered 404
		}
		return &id, true
	}
	ok := true
	var v *uuid.UUID
	if v, ok = orgRef(req.UnitID, "unit_id", func(id uuid.UUID) error { _, e := findByIDAndOrg[models.Unit](a.DB, r, id, orgID, "Unit"); return e }); !ok {
		return nil
	} else if v != nil {
		unitID = v
	}
	if v, ok = orgRef(req.DepartmentID, "department_id", func(id uuid.UUID) error { _, e := findByIDAndOrg[models.Department](a.DB, r, id, orgID, "Department"); return e }); !ok {
		return nil
	} else if v != nil {
		deptID = v
	}
	// A team belongs to one unit and department: an explicit choice must agree with it.
	if teamUnitID != nil && unitID != nil && *teamUnitID != *unitID {
		return bad("unit_id does not match the team's unit")
	}
	if teamDeptID != nil && deptID != nil && *teamDeptID != *deptID {
		return bad("department_id does not match the team's department")
	}
	if teamID == nil && deptID == nil {
		return bad("Choose the team or department that will handle the ticket")
	}
	var categoryID, reqDeptID *uuid.UUID
	if categoryID, ok = orgRef(req.CategoryID, "category_id", func(id uuid.UUID) error { _, e := findByIDAndOrg[models.OccurrenceCategory](a.DB, r, id, orgID, "Category"); return e }); !ok {
		return nil
	}
	if reqDeptID, ok = orgRef(req.RequesterDepartmentID, "requester_department_id", func(id uuid.UUID) error { _, e := findByIDAndOrg[models.Department](a.DB, r, id, orgID, "Department"); return e }); !ok {
		return nil
	}

	var assignee *uuid.UUID
	if req.AssignedUserID != nil && *req.AssignedUserID != "" {
		assignee, err = a.resolveAssignee(orgID, req.AssignedUserID)
		if err != nil {
			return bad(err.Error())
		}
	}

	if assignee != nil && teamID != nil {
		var n int64
		a.DB.Model(&models.TeamMember{}).Where("team_id = ? AND user_id = ?", *teamID, *assignee).Count(&n)
		if n == 0 {
			return bad("assigned_user_id is not a member of the team")
		}
	}

	observers := make([]uuid.UUID, 0, len(req.ObserverUserIDs))
	for _, raw := range req.ObserverUserIDs {
		id, err := a.resolveAssignee(orgID, &raw)
		if err != nil || id == nil {
			return bad("Invalid observer_user_ids")
		}
		if *id != userID { // the requester already follows their own ticket
			observers = append(observers, *id)
		}
	}

	stage, err := a.initialStageFor(orgID, teamID)
	if err != nil {
		a.Log.Error("Failed to resolve initial stage", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to resolve initial stage", nil, "")
	}

	occ := models.Occurrence{
		OrganizationID:        orgID,
		Kind:                  models.OccurrenceKindInternal,
		Source:                "manual",
		Title:                 req.Title,
		Description:           req.Description,
		StageID:               stage.ID,
		GeneralState:          stage.EffectiveState(),
		Priority:              priority,
		TicketType:            req.TicketType,
		Impact:                req.Impact,
		Urgency:               req.Urgency,
		AffectedItem:          strings.TrimSpace(req.AffectedItem),
		DueDate:               dueDate,
		OpenedByUserID:        userID,
		TeamID:                teamID,
		UnitID:                unitID,
		DepartmentID:          deptID,
		CategoryID:            categoryID,
		RequesterDepartmentID: reqDeptID,
		AssignedUserID:        assignee,
	}
	// Resolution deadline only. A first-response deadline measures the first public
	// reply to a customer, which an internal ticket does not have, so it would always
	// be marked breached. The first-attention metric is left to the functional spec.
	if _, resolutionMin, err := a.resolveOccurrenceSLAMinutes(orgID, priority, nil); err != nil {
		a.Log.Error("Failed to resolve SLA policy", "error", err, "organization_id", orgID)
	} else {
		xd := time.Now().Add(time.Duration(resolutionMin) * time.Minute)
		occ.SLA = models.SLATracking{ResolutionDeadline: &xd}
	}

	var hooks []func(tx *gorm.DB, o *models.Occurrence) error
	if teamID != nil {
		// Same trail as a case forwarded to a team: "forwarded" event, initial
		// transfer, and the requester as a participant when outside the team.
		hooks = append(hooks, a.forwardArtifacts(userID, nil, a.userInTeam(userID, *teamID)))
	}
	if len(observers) > 0 {
		hooks = append(hooks, func(tx *gorm.DB, o *models.Occurrence) error {
			for _, id := range observers {
				if err := tx.Create(&models.OccurrenceParticipant{
					OrganizationID: o.OrganizationID, OccurrenceID: o.ID, UserID: id, AddedByID: &userID,
					Role: models.ParticipantRoleFollower, Capabilities: []string{models.OccurrenceCapComment},
				}).Error; err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err := a.insertOccurrenceWithProtocol(&occ, hooks...); err != nil {
		a.Log.Error("Failed to create internal ticket", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create ticket", nil, "")
	}

	a.DB.Preload("Stage").Preload("AssignedUser").
		Preload("Unit").Preload("Department").Preload("Category").First(&occ, occ.ID)
	resp := occurrenceToResponse(occ)
	a.fillRequesterNames(orgID, &resp)
	a.broadcastOccurrence(orgID, occ.ID, websocket.WSMessage{Type: websocket.TypeOccurrenceChanged, Payload: resp})
	return r.SendEnvelope(resp)
}

// fillRequesterNames sets the requester's and requester department's names. They
// are plain lookups, not GORM relations: a relation would make AutoMigrate add
// foreign keys to the populated occurrences table.
func (a *App) fillRequesterNames(orgID uuid.UUID, resps ...*OccurrenceResponse) {
	if len(resps) == 0 {
		return
	}
	userIDs, deptIDs := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	for _, r := range resps {
		userIDs[r.OpenedByUserID] = true
		if r.RequesterDepartmentID != nil {
			deptIDs[*r.RequesterDepartmentID] = true
		}
	}
	users, depts := map[uuid.UUID]string{}, map[uuid.UUID]string{}
	var us []models.User
	a.DB.Select("id", "full_name").Where("organization_id = ? AND id IN ?", orgID, keysOf(userIDs)).Find(&us)
	for _, u := range us {
		users[u.ID] = u.FullName
	}
	if len(deptIDs) > 0 {
		var ds []models.Department
		a.DB.Select("id", "name").Where("organization_id = ? AND id IN ?", orgID, keysOf(deptIDs)).Find(&ds)
		for _, d := range ds {
			depts[d.ID] = d.Name
		}
	}
	for i := range resps {
		resps[i].OpenedByName = users[resps[i].OpenedByUserID]
		if resps[i].RequesterDepartmentID != nil {
			resps[i].RequesterDepartmentName = depts[*resps[i].RequesterDepartmentID]
		}
	}
}

func keysOf(m map[uuid.UUID]bool) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
