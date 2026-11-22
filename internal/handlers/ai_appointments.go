package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/caldav"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Scheduling by the AI agent, in the organization's Nextcloud calendar.
//
// When the agent has qualified a customer and the organization has a calendar enabled, the agent does not
// transfer at once: the SERVER reads when the calendar is free, offers up to three times as buttons, and
// books the one the customer taps. The model never picks, writes or confirms a time: what it says is limited
// to the usual conversation, and a time exists only if the calendar said it was free and the customer tapped
// it. If anything fails (no calendar, no free time, the calendar down, the time taken meanwhile) the customer
// goes to people exactly as before.

const (
	aiAppointmentPrefix  = "apt:"
	aiAppointmentSlots   = 3
	aiAppointmentTTL     = 24 * time.Hour
	aiAppointmentRetries = 1 // new offers after a chosen time turned out taken
	aiAppointmentTimeout = 15 * time.Second

	aiAppointmentOfferText    = "Para conversar com um de nossos advogados, escolha o melhor horário:"
	aiAppointmentNoLongerText = "Esse horário não está mais disponível."

	// handoff reasons and outcomes of the scheduling path
	aiHandoffScheduled        = "scheduled"         // the customer booked a time; people take it from here
	aiHandoffSchedulingFailed = "scheduling_failed" // no time could be booked; people take it
	aiHandoffOffered          = "offered_slots"     // outcome: times were offered, the customer decides
)

var (
	errCalBadPath = &chatHTTPError{http.StatusBadRequest, "Use the calendar address Nextcloud shows (/remote.php/dav/calendars/user/name/)", "calendar_bad_path"}
	errCalInvalid = &chatHTTPError{http.StatusBadRequest, "Invalid calendar settings", "calendar_invalid"}
)

var ptWeekday = [...]string{"Dom", "Seg", "Ter", "Qua", "Qui", "Sex", "Sáb"}

// slotLabel is the button title: "Seg 24/11 às 14:30" (WhatsApp allows 20 characters).
func slotLabel(t time.Time) string {
	return fmt.Sprintf("%s %02d/%02d às %02d:%02d", ptWeekday[t.Weekday()], t.Day(), int(t.Month()), t.Hour(), t.Minute())
}

// parseCalendarHours reads {"1":"09:00-18:00", ...} (weekday 0 = Sunday) into opening windows.
func parseCalendarHours(h models.JSONB) (map[time.Weekday][2]int, error) {
	out := map[time.Weekday][2]int{}
	for k, v := range h {
		d, err := strconv.Atoi(k)
		s, ok := v.(string)
		if err != nil || d < 0 || d > 6 || !ok {
			return nil, fmt.Errorf("invalid hours")
		}
		from, to, found := strings.Cut(s, "-")
		a, aok := clockMinutes(from)
		b, bok := clockMinutes(to)
		if !found || !aok || !bok || b <= a {
			return nil, fmt.Errorf("invalid hours for weekday %d", d)
		}
		out[time.Weekday(d)] = [2]int{a, b}
	}
	return out, nil
}

func clockMinutes(s string) (int, bool) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	hh, e1 := strconv.Atoi(h)
	mm, e2 := strconv.Atoi(m)
	if !ok || e1 != nil || e2 != nil || hh < 0 || hh > 24 || mm < 0 || mm > 59 || (hh == 24 && mm != 0) {
		return 0, false
	}
	return hh*60 + mm, true
}

// calendarFor builds the CalDAV client and the booking rules of the organization, or ok=false when it has
// no enabled calendar.
func (a *App) calendarFor(orgID uuid.UUID) (*models.NextcloudCalendar, *caldav.Client, caldav.Rules, bool) {
	var cal models.NextcloudCalendar
	if err := a.DB.Where("organization_id = ? AND enabled = ?", orgID, true).First(&cal).Error; err != nil {
		return nil, nil, caldav.Rules{}, false
	}
	var st models.NextcloudSettings
	if err := a.DB.Where("organization_id = ?", orgID).First(&st).Error; err != nil {
		return nil, nil, caldav.Rules{}, false
	}
	pass, err := crypto.Decrypt(cal.AppPassword, a.Config.App.EncryptionKey)
	if err != nil {
		return nil, nil, caldav.Rules{}, false
	}
	zone, err := time.LoadLocation(cal.TimeZone)
	if err != nil {
		zone = time.UTC
	}
	hours, err := parseCalendarHours(cal.Hours)
	if err != nil {
		return nil, nil, caldav.Rules{}, false
	}
	client := &caldav.Client{
		HTTP: a.nextcloudClient(st.BaseURL), CalendarURL: strings.TrimRight(st.BaseURL, "/") + cal.CalendarPath,
		User: cal.Username, Pass: pass, Zone: zone,
	}
	rules := caldav.Rules{
		Zone: zone, Hours: hours, Slot: time.Duration(cal.SlotMinutes) * time.Minute, Buffer: time.Duration(cal.BufferMin) * time.Minute,
		MinNotice: time.Duration(cal.MinNoticeHrs) * time.Hour, Lookahead: cal.LookaheadDay,
	}
	return &cal, client, rules, true
}

// freeSlots asks the calendar when it is free.
func freeSlots(ctx context.Context, client *caldav.Client, rules caldav.Rules, now time.Time) ([]time.Time, error) {
	to := now.AddDate(0, 0, rules.Lookahead+1)
	busy, err := client.Busy(ctx, now, to)
	if err != nil {
		return nil, err
	}
	return caldav.Slots(rules, busy, now, aiAppointmentSlots), nil
}

func newAppointmentToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// offerAppointment offers the customer free times as buttons. It returns false when it could not (no
// calendar, nothing free, the calendar down, the message not sent): the caller then hands over to people.
func (a *App) offerAppointment(account *models.WhatsAppAccount, contact *models.Contact, session *models.ChatbotSession, oppID *uuid.UUID, attempts int) bool {
	orgID := account.OrganizationID
	_, client, rules, ok := a.calendarFor(orgID)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), aiAppointmentTimeout)
	defer cancel()
	slots, err := freeSlots(ctx, client, rules, time.Now())
	if err != nil {
		a.Log.Warn("Scheduling: could not read the calendar", "error", err, "org_id", orgID)
		return false
	}
	if len(slots) == 0 {
		a.Log.Info("Scheduling: the calendar has no free time to offer", "org_id", orgID)
		return false
	}
	token, err := newAppointmentToken()
	if err != nil {
		return false
	}
	utc := make(models.StringArray, len(slots))
	buttons := make([]map[string]any, len(slots))
	for i, s := range slots {
		utc[i] = s.UTC().Format(time.RFC3339)
		buttons[i] = map[string]any{"id": fmt.Sprintf("%s%s:%d", aiAppointmentPrefix, token, i), "title": slotLabel(s.In(rules.Zone))}
	}
	// a newer offer replaces any older one still open for this customer
	a.DB.Model(&models.AIAppointmentOffer{}).
		Where("organization_id = ? AND contact_id = ? AND status = ?", orgID, contact.ID, models.AIAppointmentOffered).
		Update("status", models.AIAppointmentExpired)
	offer := models.AIAppointmentOffer{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID, ContactID: contact.ID, SessionID: session.ID,
		WhatsAppAccount: account.Name, OpportunityID: oppID, Token: token, Slots: utc,
		Status: models.AIAppointmentOffered, ExpiresAt: time.Now().Add(aiAppointmentTTL), Attempts: attempts,
	}
	if err := a.DB.Create(&offer).Error; err != nil {
		a.Log.Error("Scheduling: could not record the offer", "error", err)
		return false
	}
	if err := a.sendAndSaveInteractiveButtons(aiCtx("ai_appointment_offer"), account, contact, aiAppointmentOfferText, buttons); err != nil {
		a.Log.Error("Scheduling: could not send the offer", "error", err, "contact_id", contact.ID)
		a.DB.Model(&offer).Update("status", models.AIAppointmentExpired)
		return false
	}
	return true
}

func isAppointmentButton(id string) bool { return strings.HasPrefix(id, aiAppointmentPrefix) }

// handleAppointmentTap books the time the customer tapped. The tap is consumed here, it never reaches the
// chatbot as a message. Booking is atomic: of two taps (or a retried webhook) only one claims the offer.
func (a *App) handleAppointmentTap(account *models.WhatsAppAccount, contact *models.Contact, buttonID string) {
	parts := strings.Split(strings.TrimPrefix(buttonID, aiAppointmentPrefix), ":")
	if len(parts) != 2 {
		return
	}
	idx, err := strconv.Atoi(parts[1])
	if err != nil {
		return
	}
	orgID := account.OrganizationID
	var offer models.AIAppointmentOffer
	if err := a.DB.Where("organization_id = ? AND contact_id = ? AND token = ?", orgID, contact.ID, parts[0]).First(&offer).Error; err != nil {
		return // not an offer of this customer: nothing to say
	}
	if idx < 0 || idx >= len(offer.Slots) {
		return
	}
	// claim: only one tap turns "offered" into "booked"
	claim := a.DB.Model(&models.AIAppointmentOffer{}).
		Where("id = ? AND status = ? AND expires_at > ?", offer.ID, models.AIAppointmentOffered, time.Now()).
		Update("status", models.AIAppointmentBooked)
	if claim.Error != nil || claim.RowsAffected == 0 {
		return // already used, replaced or expired: a repeated tap says nothing
	}
	start, err := time.Parse(time.RFC3339, offer.Slots[idx])
	if err != nil {
		a.failAppointment(account, contact, &offer, "invalid slot")
		return
	}
	_, client, rules, ok := a.calendarFor(orgID)
	if !ok {
		a.failAppointment(account, contact, &offer, "calendar not available")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), aiAppointmentTimeout)
	defer cancel()
	end := start.Add(rules.Slot)
	// the time was free when offered; check again, another booking may have taken it meanwhile
	busy, err := client.Busy(ctx, start.Add(-rules.Buffer), end.Add(rules.Buffer))
	if err != nil {
		a.failAppointment(account, contact, &offer, "calendar read failed")
		return
	}
	for _, b := range busy {
		if b.Start.Before(end.Add(rules.Buffer)) && start.Add(-rules.Buffer).Before(b.End) {
			a.retryAppointment(account, contact, &offer, aiAppointmentNoLongerText)
			return
		}
	}
	uid := uuid.NewString() + "@whatc"
	ev := caldav.Event{
		UID: uid, Summary: "Conversa com " + a.appointmentCustomerName(contact), Start: start, End: end,
		Description: a.appointmentDescription(contact, &offer), Location: a.appointmentLocation(orgID),
	}
	switch err := client.CreateEvent(ctx, ev); err {
	case nil:
	case caldav.ErrConflict:
		a.retryAppointment(account, contact, &offer, aiAppointmentNoLongerText)
		return
	default:
		a.failAppointment(account, contact, &offer, "event not created")
		return
	}
	a.DB.Model(&models.AIAppointmentOffer{}).Where("id = ?", offer.ID).Updates(map[string]any{"booked_start": start, "event_uid": uid})

	local := start.In(rules.Zone)
	a.markOpportunityScheduled(&offer, local)
	text := fmt.Sprintf("Agendado! Sua conversa com o advogado é %s, %02d/%02d às %02d:%02d. Se precisar remarcar, é só escrever aqui.",
		strings.ToLower(ptWeekday[local.Weekday()]), local.Day(), int(local.Month()), local.Hour(), local.Minute())
	if err := a.sendAndSaveAgentText(aiCtx("ai_appointment_booked"), account, contact, text, a.appointmentSender(account, &offer)); err != nil {
		a.Log.Error("Scheduling: could not confirm the booking to the customer", "error", err, "contact_id", contact.ID)
	}
	a.handOverAfterScheduling(account, contact, &offer, aiHandoffScheduled)
}

// retryAppointment tells the customer the time is gone and offers new ones (once); then people take it.
func (a *App) retryAppointment(account *models.WhatsAppAccount, contact *models.Contact, offer *models.AIAppointmentOffer, why string) {
	a.DB.Model(&models.AIAppointmentOffer{}).Where("id = ?", offer.ID).Update("status", models.AIAppointmentExpired)
	if err := a.sendAndSaveAgentText(aiCtx("ai_appointment_taken"), account, contact, why, a.appointmentSender(account, offer)); err != nil {
		a.Log.Error("Scheduling: could not answer the customer", "error", err)
	}
	if offer.Attempts < aiAppointmentRetries {
		var session models.ChatbotSession
		if a.DB.First(&session, "id = ?", offer.SessionID).Error == nil &&
			a.offerAppointment(account, contact, &session, offer.OpportunityID, offer.Attempts+1) {
			return
		}
	}
	a.handOverAfterScheduling(account, contact, offer, aiHandoffSchedulingFailed)
}

// failAppointment is a failure on our side (the calendar): the customer goes to people.
func (a *App) failAppointment(account *models.WhatsAppAccount, contact *models.Contact, offer *models.AIAppointmentOffer, why string) {
	a.Log.Warn("Scheduling failed; handing over to people", "reason", why, "contact_id", contact.ID)
	a.DB.Model(&models.AIAppointmentOffer{}).Where("id = ?", offer.ID).Update("status", models.AIAppointmentExpired)
	a.handOverAfterScheduling(account, contact, offer, aiHandoffSchedulingFailed)
}

// handOverAfterScheduling gives the attendance to people through the same handoff the agent always uses.
func (a *App) handOverAfterScheduling(account *models.WhatsAppAccount, contact *models.Contact, offer *models.AIAppointmentOffer, reason string) {
	var session models.ChatbotSession
	if err := a.DB.First(&session, "id = ?", offer.SessionID).Error; err != nil {
		return
	}
	settings, err := a.getChatbotSettingsCached(account.OrganizationID, account.Name)
	if err != nil || settings == nil {
		return
	}
	a.handoffFromAI(account, contact, &session, settings, reason)
}

func (a *App) appointmentSender(account *models.WhatsAppAccount, offer *models.AIAppointmentOffer) *AISender {
	var session models.ChatbotSession
	if a.DB.First(&session, "id = ?", offer.SessionID).Error != nil {
		return nil
	}
	settings, err := a.getChatbotSettingsCached(account.OrganizationID, account.Name)
	if err != nil || settings == nil {
		return nil
	}
	return newAISender(a.agentForSession(settings, &session))
}

func (a *App) appointmentCustomerName(contact *models.Contact) string {
	if n := strings.TrimSpace(contact.ProfileName); n != "" {
		return n
	}
	return "cliente"
}

func (a *App) appointmentLocation(orgID uuid.UUID) string {
	var cal models.NextcloudCalendar
	if a.DB.Select("location").Where("organization_id = ?", orgID).First(&cal).Error != nil {
		return ""
	}
	return cal.Location
}

// appointmentDescription is what the lawyer reads on the event: who, how to reach them, what was qualified.
func (a *App) appointmentDescription(contact *models.Contact, offer *models.AIAppointmentOffer) string {
	var b strings.Builder
	b.WriteString("Agendado pelo agente de IA pelo WhatsApp.\n")
	b.WriteString("Cliente: " + a.appointmentCustomerName(contact) + "\n")
	b.WriteString("Telefone: " + contact.PhoneNumber + "\n")
	if offer.OpportunityID != nil {
		var opp models.SalesOpportunity
		if a.DB.Select("interest").Where("id = ?", *offer.OpportunityID).First(&opp).Error == nil && opp.Interest != "" {
			b.WriteString("\n" + opp.Interest + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// markOpportunityScheduled writes the meeting on the opportunity the agent opened, so the CRM shows it.
func (a *App) markOpportunityScheduled(offer *models.AIAppointmentOffer, local time.Time) {
	if offer.OpportunityID == nil {
		return
	}
	line := fmt.Sprintf("\n\nReunião agendada: %02d/%02d/%d às %02d:%02d.", local.Day(), int(local.Month()), local.Year(), local.Hour(), local.Minute())
	a.DB.Model(&models.SalesOpportunity{}).Where("id = ?", *offer.OpportunityID).
		Update("interest", gorm.Expr("interest || ?", line))
}

// --- settings of the calendar -----------------------------------------------------------------------

type calendarRequest struct {
	Enabled      *bool        `json:"enabled"`
	CalendarPath *string      `json:"calendar_path"`
	Username     *string      `json:"username"`
	AppPassword  *string      `json:"app_password"` // empty keeps the stored one
	TimeZone     *string      `json:"time_zone"`
	SlotMinutes  *int         `json:"slot_minutes"`
	BufferMin    *int         `json:"buffer_minutes"`
	MinNoticeHrs *int         `json:"min_notice_hours"`
	LookaheadDay *int         `json:"lookahead_days"`
	Hours        models.JSONB `json:"hours"`
	Location     *string      `json:"location"`
}

// GetNextcloudCalendar returns the calendar settings of the organization (never the password).
func (a *App) GetNextcloudCalendar(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionRead)
	if err != nil {
		return nil
	}
	var cal models.NextcloudCalendar
	if a.DB.Where("organization_id = ?", orgID).First(&cal).Error != nil {
		return r.SendEnvelope(map[string]any{"configured": false})
	}
	return r.SendEnvelope(map[string]any{"configured": true, "calendar": cal, "password_set": cal.AppPassword != ""})
}

// PutNextcloudCalendar saves the calendar settings. When enabling, it reads the calendar once to prove the
// address and the credentials work, so a typo is found here and not in front of a customer.
func (a *App) PutNextcloudCalendar(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req calendarRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	var st models.NextcloudSettings
	if a.DB.Where("organization_id = ?", orgID).First(&st).Error != nil {
		return a.sendChatError(r, errNextcloudNotConfigured, "save the calendar")
	}
	var cal models.NextcloudCalendar
	exists := a.DB.Where("organization_id = ?", orgID).First(&cal).Error == nil
	if !exists {
		cal = models.NextcloudCalendar{ID: uuid.New(), OrganizationID: orgID, TimeZone: "America/Sao_Paulo", SlotMinutes: 30, MinNoticeHrs: 2, LookaheadDay: 14, Hours: models.JSONB{}}
	}
	if req.CalendarPath != nil {
		p, ok := cleanNextcloudPath(*req.CalendarPath)
		if !ok || !strings.HasPrefix(p, "/remote.php/dav/calendars/") || strings.Count(strings.Trim(p, "/"), "/") < 4 {
			return a.sendChatError(r, errCalBadPath, "save the calendar")
		}
		cal.CalendarPath = strings.TrimRight(p, "/") + "/"
	}
	if req.Username != nil {
		cal.Username = strings.TrimSpace(*req.Username)
	}
	if req.AppPassword != nil && *req.AppPassword != "" {
		enc, eerr := crypto.Encrypt(*req.AppPassword, a.Config.App.EncryptionKey)
		if eerr != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save the calendar", nil, "")
		}
		cal.AppPassword = enc
	}
	if req.TimeZone != nil {
		if _, lerr := time.LoadLocation(*req.TimeZone); lerr != nil {
			return a.sendChatError(r, errCalInvalid, "save the calendar")
		}
		cal.TimeZone = *req.TimeZone
	}
	intIn := func(p *int, dst *int, lo, hi int) bool {
		if p == nil {
			return true
		}
		if *p < lo || *p > hi {
			return false
		}
		*dst = *p
		return true
	}
	if !intIn(req.SlotMinutes, &cal.SlotMinutes, 15, 240) || !intIn(req.BufferMin, &cal.BufferMin, 0, 120) ||
		!intIn(req.MinNoticeHrs, &cal.MinNoticeHrs, 0, 168) || !intIn(req.LookaheadDay, &cal.LookaheadDay, 1, 60) {
		return a.sendChatError(r, errCalInvalid, "save the calendar")
	}
	if req.Hours != nil {
		if _, herr := parseCalendarHours(req.Hours); herr != nil {
			return a.sendChatError(r, errCalInvalid, "save the calendar")
		}
		cal.Hours = req.Hours
	}
	if req.Location != nil {
		cal.Location = strings.TrimSpace(*req.Location)
	}
	if req.Enabled != nil {
		cal.Enabled = *req.Enabled
	}
	if cal.Enabled {
		if cal.CalendarPath == "" || cal.Username == "" || cal.AppPassword == "" {
			return a.sendChatError(r, errCalInvalid, "enable the calendar")
		}
		if cerr := a.probeCalendar(&cal, st.BaseURL); cerr != nil {
			return a.sendChatError(r, cerr, "enable the calendar")
		}
	}
	cal.UpdatedByID = &userID
	if err := a.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&cal).Error; err != nil {
		a.Log.Error("Failed to save the calendar settings", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save the calendar", nil, "")
	}
	return r.SendEnvelope(map[string]any{"configured": true, "calendar": cal, "password_set": true})
}

// probeCalendar reads one day of the calendar: it proves address, user and password.
func (a *App) probeCalendar(cal *models.NextcloudCalendar, base string) *chatHTTPError {
	pass, err := crypto.Decrypt(cal.AppPassword, a.Config.App.EncryptionKey)
	if err != nil {
		return errCalInvalid
	}
	c := &caldav.Client{HTTP: a.nextcloudClient(base), CalendarURL: strings.TrimRight(base, "/") + cal.CalendarPath, User: cal.Username, Pass: pass}
	ctx, cancel := context.WithTimeout(context.Background(), aiAppointmentTimeout)
	defer cancel()
	_, err = c.Busy(ctx, time.Now(), time.Now().Add(24*time.Hour))
	switch err {
	case nil:
		return nil
	case caldav.ErrAuth:
		return errNextcloudAuth
	case caldav.ErrNotFound:
		return &chatHTTPError{http.StatusNotFound, "Calendar not found: check the address", "calendar_not_found"}
	}
	return errNextcloudUnreachable
}

// TestNextcloudCalendar proves the calendar works end to end without leaving anything behind: it reads the
// free times the agent would offer, writes one test event at the first of them and removes it at once. It is
// what tells the administrator the user may WRITE here (reading alone, which saving checks, does not).
func (a *App) TestNextcloudCalendar(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}
	_, client, rules, ok := a.calendarFor(orgID)
	if !ok {
		return a.sendChatError(r, &chatHTTPError{http.StatusConflict, "Enable the calendar first", "calendar_not_enabled"}, "test the calendar")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*aiAppointmentTimeout)
	defer cancel()
	slots, err := freeSlots(ctx, client, rules, time.Now())
	if err != nil {
		return a.sendChatError(r, calendarErrorFor(err), "test the calendar")
	}
	labels := make([]string, len(slots))
	for i, s := range slots {
		labels[i] = slotLabel(s.In(rules.Zone))
	}
	out := map[string]any{"slots": labels, "write_checked": false}
	if len(slots) > 0 {
		uid := uuid.NewString() + "@whatc-test"
		ev := caldav.Event{UID: uid, Summary: "Teste do Whatc (removido em seguida)", Start: slots[0], End: slots[0].Add(rules.Slot),
			Description: "Evento criado e removido pelo teste da agenda do Whatc."}
		if err := client.CreateEvent(ctx, ev); err != nil {
			return a.sendChatError(r, calendarErrorFor(err), "test the calendar")
		}
		if err := client.DeleteEvent(ctx, uid); err != nil {
			// the write worked but the clean-up did not: say so, the event has to be removed by hand
			a.Log.Warn("Calendar test: the test event could not be removed", "error", err, "uid", uid)
			out["cleanup_failed"] = true
		}
		out["write_checked"] = true
	}
	return r.SendEnvelope(out)
}

// calendarErrorFor maps what the calendar answered to the error the screen explains.
func calendarErrorFor(err error) *chatHTTPError {
	switch {
	case errors.Is(err, caldav.ErrAuth):
		return errNextcloudAuth
	case errors.Is(err, caldav.ErrNotFound):
		return &chatHTTPError{http.StatusNotFound, "Calendar not found: check the address", "calendar_not_found"}
	case errors.Is(err, caldav.ErrReadOnly):
		return &chatHTTPError{http.StatusUnprocessableEntity, "This user cannot add events to the calendar", "calendar_read_only"}
	}
	return errNextcloudUnreachable
}
