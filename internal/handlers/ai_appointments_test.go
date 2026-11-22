package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
)

// calServer is a Nextcloud calendar as the agent sees it: it answers REPORT with whatever is busy and
// records every event written to it.
type calServer struct {
	mu     sync.Mutex
	busy   []time.Time // each busy for one hour
	status int         // REPORT status (0 = 207)
	puts   []string
	dels   []string // paths of the events removed
	putSt  int      // status of a PUT (0 = 201 Created)
	srv    *httptest.Server
}

func newCalServer(t *testing.T) *calServer {
	t.Helper()
	c := &calServer{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		switch r.Method {
		case "REPORT":
			if c.status != 0 {
				w.WriteHeader(c.status)
				return
			}
			var b strings.Builder
			b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">`)
			for i, s := range c.busy {
				fmt.Fprintf(&b, `<d:response><d:href>/e%d.ics</d:href><d:propstat><d:prop><c:calendar-data>BEGIN:VCALENDAR
BEGIN:VEVENT
UID:%d
DTSTART:%s
DTEND:%s
END:VEVENT
END:VCALENDAR</c:calendar-data></d:prop></d:propstat></d:response>`, i, i, s.UTC().Format("20060102T150405Z"), s.Add(time.Hour).UTC().Format("20060102T150405Z"))
			}
			b.WriteString(`</d:multistatus>`)
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(b.String()))
		case "PUT":
			body, _ := io.ReadAll(r.Body)
			if c.putSt != 0 {
				w.WriteHeader(c.putSt)
				return
			}
			c.puts = append(c.puts, string(body))
			w.WriteHeader(http.StatusCreated)
		case "DELETE":
			c.dels = append(c.dels, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *calServer) putCount() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.puts) }
func (c *calServer) setBusy(b ...time.Time) {
	c.mu.Lock()
	c.busy = b
	c.mu.Unlock()
}
func (c *calServer) setStatus(s int) { c.mu.Lock(); c.status = s; c.mu.Unlock() }

// withCalendar turns the organization's calendar on, pointing at cal.
func (w *wiring) withCalendar(t *testing.T, cal *calServer) {
	t.Helper()
	w.app.Config.App.AllowPrivateNextcloud = true // the fake calendar is on 127.0.0.1
	require.NoError(t, w.app.DB.Create(&models.NextcloudSettings{ID: uuid.New(), OrganizationID: w.f.org.ID, BaseURL: cal.srv.URL}).Error)
	enc, err := crypto.Encrypt("app-pass", w.app.Config.App.EncryptionKey)
	require.NoError(t, err)
	hours := models.JSONB{}
	for d := 0; d < 7; d++ {
		hours[fmt.Sprint(d)] = "00:00-24:00" // always open: the test must not depend on the time it runs
	}
	require.NoError(t, w.app.DB.Create(&models.NextcloudCalendar{
		ID: uuid.New(), OrganizationID: w.f.org.ID, Enabled: true, CalendarPath: "/remote.php/dav/calendars/triagem/personal/",
		Username: "triagem", AppPassword: enc, TimeZone: "America/Sao_Paulo", SlotMinutes: 30, MinNoticeHrs: 0, LookaheadDay: 7,
		Hours: hours, Location: "Escritório Centro",
	}).Error)
}

var aptButtonRE = regexp.MustCompile(`apt:[0-9a-f]{32}:\d`)

func (w *wiring) offeredButtons() []string {
	var out []string
	for _, b := range w.textSends() {
		out = append(out, aptButtonRE.FindAllString(b, -1)...)
	}
	return out
}

func (w *wiring) offers(t *testing.T, contactID uuid.UUID) []models.AIAppointmentOffer {
	t.Helper()
	var rows []models.AIAppointmentOffer
	require.NoError(t, w.app.DB.Where("contact_id = ?", contactID).Order("created_at").Find(&rows).Error)
	return rows
}

var aptCustomer = []string{"Oi, quero porcelanato", "para a sala, uns 40 m²"} // what qualHappy quotes

func newScheduler(t *testing.T) (*wiring, *calServer, *models.Contact, *models.ChatbotSession) {
	t.Helper()
	w := newWiring(t, qualHappy)
	cal := newCalServer(t)
	w.withCalendar(t, cal)
	contact, session := w.handoffSession(t, aptCustomer...)
	return w, cal, contact, session
}

func TestScheduling_TheAgentOffersFreeTimesInsteadOfCallingAPersonAtOnce(t *testing.T) {
	w, _, contact, session := newScheduler(t)
	out := w.app.handoffFromAIOutcome(w.f.account, contact, session, w.f.settings, aiHandoffMarker)

	assert.Equal(t, aiHandoffOffered, out)
	assert.Empty(t, w.transfers(t, contact.ID), "nobody was called yet: the customer picks a time first")
	assert.Equal(t, 0, countContaining(w.textSends(), aiHandoffWaitMessage[:20]), "no \"vou chamar um atendente\"")
	require.Len(t, w.offeredButtons(), 3, "three times as buttons")
	assert.Contains(t, strings.Join(w.textSends(), "\n"), "escolha o melhor horário")

	offers := w.offers(t, contact.ID)
	require.Len(t, offers, 1)
	assert.Equal(t, models.AIAppointmentOffered, offers[0].Status)
	assert.Len(t, offers[0].Slots, 3)
	assert.Len(t, w.opportunities(t, contact.ID), 1, "the lead is in the CRM already")
}

func TestScheduling_TappingATimeBooksItAndTheAgentStaysInCharge(t *testing.T) {
	w, cal, contact, session := newScheduler(t)
	require.Equal(t, aiHandoffOffered, w.app.handoffFromAIOutcome(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	buttons := w.offeredButtons()
	offer := w.offers(t, contact.ID)[0]

	w.app.handleAppointmentTap(w.f.account, contact, buttons[1])

	require.Equal(t, 1, cal.putCount())
	ics := strings.ReplaceAll(cal.puts[0], "\r\n ", "") // unfolded
	want, _ := time.Parse(time.RFC3339, offer.Slots[1])
	assert.Contains(t, ics, "DTSTART:"+want.UTC().Format("20060102T150405Z"), "the time the customer tapped")
	assert.Contains(t, ics, "Telefone: "+contact.PhoneNumber)
	assert.Contains(t, ics, "LOCATION:Escritório Centro", "carries the location")
	assert.Contains(t, ics, "porcelanato", "the lawyer reads what was qualified")

	got := w.offers(t, contact.ID)[0]
	assert.Equal(t, models.AIAppointmentBooked, got.Status)
	assert.NotEmpty(t, got.EventUID)
	assert.Contains(t, strings.Join(w.textSends(), "\n"), "Agendado!")
	assert.Empty(t, w.transfers(t, contact.ID), "no attendance is opened: the agent stays in charge of the conversation")
	assert.Equal(t, 0, countContaining(w.textSends(), aiHandoffWaitMessage[:20]), "no \"vou chamar um atendente\" either")
	require.NotNil(t, w.app.upcomingAppointment(w.f.org.ID, contact.ID), "the meeting is known to the system")
	assert.Contains(t, w.opportunities(t, contact.ID)[0].Interest, "Reunião agendada")
}

func TestScheduling_ARepeatedTapBooksOnce(t *testing.T) {
	w, cal, contact, session := newScheduler(t)
	require.Equal(t, aiHandoffOffered, w.app.handoffFromAIOutcome(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	b := w.offeredButtons()[0]

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); w.app.handleAppointmentTap(w.f.account, contact, b) }()
	}
	wg.Wait()
	assert.Equal(t, 1, cal.putCount(), "five taps, one event")
	assert.Empty(t, w.transfers(t, contact.ID))
}

func TestScheduling_ATimeTakenMeanwhileIsNotBookedAndNewTimesAreOffered(t *testing.T) {
	w, cal, contact, session := newScheduler(t)
	require.Equal(t, aiHandoffOffered, w.app.handoffFromAIOutcome(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	offer := w.offers(t, contact.ID)[0]
	taken, _ := time.Parse(time.RFC3339, offer.Slots[0])
	cal.setBusy(taken) // somebody booked it while the customer was thinking

	w.app.handleAppointmentTap(w.f.account, contact, w.offeredButtons()[0])

	assert.Equal(t, 0, cal.putCount(), "never double-books")
	assert.Contains(t, strings.Join(w.textSends(), "\n"), "não está mais disponível")
	offers := w.offers(t, contact.ID)
	require.Len(t, offers, 2, "a fresh offer")
	assert.Equal(t, models.AIAppointmentExpired, offers[0].Status)
	assert.Equal(t, models.AIAppointmentOffered, offers[1].Status)
	assert.Equal(t, 1, offers[1].Attempts)
	assert.Empty(t, w.transfers(t, contact.ID), "still the customer's choice")
}

func TestScheduling_ASecondTakenTimeHandsOverToPeople(t *testing.T) {
	w, cal, contact, session := newScheduler(t)
	require.Equal(t, aiHandoffOffered, w.app.handoffFromAIOutcome(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	for round := 0; round < 2; round++ {
		offers := w.offers(t, contact.ID)
		cur := offers[len(offers)-1]
		taken, _ := time.Parse(time.RFC3339, cur.Slots[0])
		cal.setBusy(taken)
		w.app.handleAppointmentTap(w.f.account, contact, fmt.Sprintf("apt:%s:0", cur.Token))
	}
	assert.Equal(t, 0, cal.putCount())
	assert.Len(t, w.transfers(t, contact.ID), 1, "after two taken times a person takes over")
}

func TestScheduling_ACalendarThatIsDownFallsBackToThePeopleHandoff(t *testing.T) {
	w, cal, contact, session := newScheduler(t)
	cal.setStatus(http.StatusInternalServerError)

	out := w.app.handoffFromAIOutcome(w.f.account, contact, session, w.f.settings, aiHandoffMarker)

	assert.Equal(t, aiHandoffTransferred, out, "exactly what happens with no calendar")
	assert.Len(t, w.transfers(t, contact.ID), 1)
	assert.Equal(t, 1, countContaining(w.textSends(), aiHandoffWaitMessage[:20]))
	assert.Empty(t, w.offeredButtons())
	assert.Empty(t, w.offers(t, contact.ID))
}

func TestScheduling_ANoFreeTimeFallsBackToPeople(t *testing.T) {
	w, cal, contact, session := newScheduler(t)
	now := time.Now()
	var all []time.Time
	for h := 0; h < 24*9; h++ {
		all = append(all, now.Add(time.Duration(h-1)*time.Hour))
	}
	cal.setBusy(all...)
	assert.Equal(t, aiHandoffTransferred, w.app.handoffFromAIOutcome(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
}

func TestScheduling_ATapOfAnotherCustomerOrAnExpiredOfferDoesNothing(t *testing.T) {
	w, cal, contact, session := newScheduler(t)
	require.Equal(t, aiHandoffOffered, w.app.handoffFromAIOutcome(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	b := w.offeredButtons()[0]

	other, _ := w.handoffSession(t, "Oi")
	w.app.handleAppointmentTap(w.f.account, other, b) // someone else's button
	assert.Equal(t, 0, cal.putCount())

	require.NoError(t, w.app.DB.Model(&models.AIAppointmentOffer{}).Where("contact_id = ?", contact.ID).
		Update("expires_at", time.Now().Add(-time.Minute)).Error)
	w.app.handleAppointmentTap(w.f.account, contact, b)
	assert.Equal(t, 0, cal.putCount(), "an old offer is not honoured")

	w.app.handleAppointmentTap(w.f.account, contact, "apt:"+strings.Repeat("0", 32)+":0") // a token that never existed
	w.app.handleAppointmentTap(w.f.account, contact, "apt:garbage")
	assert.Equal(t, 0, cal.putCount())
}

func TestScheduling_WithoutACalendarTheHandoffIsExactlyAsBefore(t *testing.T) {
	w := newWiring(t, qualHappy)
	contact, session := w.handoffSession(t, aptCustomer...)
	assert.Equal(t, aiHandoffTransferred, w.app.handoffFromAIOutcome(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	assert.Empty(t, w.offers(t, contact.ID))
}

func TestCalendarHours_AreValidated(t *testing.T) {
	ok, err := parseCalendarHours(models.JSONB{"1": "09:00-18:00", "6": "08:30-12:00"})
	require.NoError(t, err)
	assert.Equal(t, [2]int{540, 1080}, ok[time.Monday])
	for _, bad := range []models.JSONB{{"7": "09:00-18:00"}, {"1": "18:00-09:00"}, {"1": "9-18"}, {"x": "09:00-18:00"}, {"1": 5}} {
		_, err := parseCalendarHours(bad)
		assert.Error(t, err, "%v", bad)
	}
	assert.Equal(t, "Ter 24/11 às 14:30", slotLabel(time.Date(2026, 11, 24, 14, 30, 0, 0, time.UTC)))
	assert.LessOrEqual(t, len(slotLabel(time.Date(2026, 12, 31, 23, 30, 0, 0, time.UTC))), 20, "a WhatsApp button title is at most 20 characters")
}

// A refused Nextcloud password must never come back as 401: the web app reads 401 as "the Whatc session expired"
// and signs the person out, which is what happened when the office's password was refused.
func TestCalendarSettings_ARefusedPasswordIsNotA401AndEnablingChecksTheCalendar(t *testing.T) {
	w := newWiring(t, "x")
	cal := newCalServer(t)
	w.app.Config.App.AllowPrivateNextcloud = true
	require.NoError(t, w.app.DB.Create(&models.NextcloudSettings{ID: uuid.New(), OrganizationID: w.f.org.ID, BaseURL: cal.srv.URL}).Error)
	role := testutil.CreateAdminRole(t, w.app.DB, w.f.org.ID)
	admin := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID, testutil.WithRoleID(&role.ID))
	put := func(body map[string]any) (int, string) {
		req := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(req, w.f.org.ID, admin.ID)
		require.NoError(t, w.app.PutNextcloudCalendar(req))
		return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
	}
	body := map[string]any{"enabled": true, "calendar_path": "/remote.php/dav/calendars/agenda/personal/", "username": "agenda", "app_password": "s3cret-pass"}

	cal.setStatus(http.StatusUnauthorized) // Nextcloud refuses the user or password
	code, raw := put(body)
	assert.Equal(t, 422, code)
	assert.NotEqual(t, 401, code)
	assert.Contains(t, raw, "nextcloud_auth")
	assert.NotContains(t, raw, "s3cret-pass")

	cal.setStatus(http.StatusNotFound)
	code, raw = put(body)
	assert.Equal(t, 404, code)
	assert.Contains(t, raw, "calendar_not_found")

	cal.setStatus(0) // the calendar answers
	code, raw = put(body)
	require.Equal(t, 200, code, raw)
	assert.NotContains(t, raw, "s3cret-pass", "the password is never returned")
	var row models.NextcloudCalendar
	require.NoError(t, w.app.DB.Where("organization_id = ?", w.f.org.ID).First(&row).Error)
	assert.True(t, row.Enabled)
	assert.NotEqual(t, "s3cret-pass", row.AppPassword, "stored encrypted")
	assert.NotEmpty(t, row.AppPassword)
}

func (c *calServer) setPutStatus(s int) { c.mu.Lock(); c.putSt = s; c.mu.Unlock() }

func TestCalendarTest_ReadsFreeTimesWritesOneEventAndRemovesIt(t *testing.T) {
	w := newWiring(t, "x")
	cal := newCalServer(t)
	w.withCalendar(t, cal)
	role := testutil.CreateAdminRole(t, w.app.DB, w.f.org.ID)
	admin := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID, testutil.WithRoleID(&role.ID))
	run := func() (int, string) {
		req := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(req, w.f.org.ID, admin.ID)
		require.NoError(t, w.app.TestNextcloudCalendar(req))
		return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
	}

	code, raw := run()
	require.Equal(t, 200, code, raw)
	assert.Contains(t, raw, `"write_checked":true`)
	assert.Regexp(t, `"slots":\["[A-Za-zÀ-ú]{3} \d\d/\d\d às \d\d:\d\d"`, raw, "the times the agent would offer")
	assert.Equal(t, 1, cal.putCount(), "one test event written")
	require.Len(t, cal.dels, 1, "and removed at once")
	assert.Contains(t, cal.dels[0], "@whatc-test.ics", "only the test event")

	cal.setPutStatus(http.StatusForbidden) // the user may read the calendar but not add to it
	code, raw = run()
	assert.Equal(t, 422, code)
	assert.Contains(t, raw, "calendar_read_only")

	cal.setStatus(http.StatusUnauthorized)
	code, raw = run()
	assert.Equal(t, 422, code, "never a 401: that would sign the person out of Whatc")
	assert.Contains(t, raw, "nextcloud_auth")
}

func TestCalendarTest_NeedsTheCalendarEnabled(t *testing.T) {
	w := newWiring(t, "x")
	role := testutil.CreateAdminRole(t, w.app.DB, w.f.org.ID)
	admin := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID, testutil.WithRoleID(&role.ID))
	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, w.f.org.ID, admin.ID)
	require.NoError(t, w.app.TestNextcloudCalendar(req))
	assert.Equal(t, 409, testutil.GetResponseStatusCode(req))
	assert.Contains(t, string(testutil.GetResponseBody(req)), "calendar_not_enabled")
}

// The opening hours the administrator ticked must survive a save and a reload, and a later save that does not
// send them must keep them.
func TestCalendarSettings_TheOpeningHoursSurviveSavingAndReloading(t *testing.T) {
	w := newWiring(t, "x")
	cal := newCalServer(t)
	w.app.Config.App.AllowPrivateNextcloud = true
	require.NoError(t, w.app.DB.Create(&models.NextcloudSettings{ID: uuid.New(), OrganizationID: w.f.org.ID, BaseURL: cal.srv.URL}).Error)
	role := testutil.CreateAdminRole(t, w.app.DB, w.f.org.ID)
	admin := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID, testutil.WithRoleID(&role.ID))
	call := func(h func(*fastglue.Request) error, body any) (int, string) {
		req := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(req, w.f.org.ID, admin.ID)
		require.NoError(t, h(req))
		return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
	}
	base := map[string]any{"enabled": true, "calendar_path": "/remote.php/dav/calendars/agente/whatsapp/", "username": "agente", "app_password": "p"}

	hours := map[string]any{"1": "09:00-18:00", "2": "09:00-18:00", "6": "08:00-12:00"}
	code, raw := call(w.app.PutNextcloudCalendar, merge(base, map[string]any{"hours": hours}))
	require.Equal(t, 200, code, raw)
	_, raw = call(w.app.GetNextcloudCalendar, map[string]any{})
	assert.Contains(t, raw, `"6":"08:00-12:00"`, "Saturday was saved")
	assert.Contains(t, raw, `"1":"09:00-18:00"`)

	// a second save of the same hours (the administrator changes the meeting length) keeps them
	code, raw = call(w.app.PutNextcloudCalendar, merge(base, map[string]any{"hours": hours, "slot_minutes": 45}))
	require.Equal(t, 200, code, raw)
	// CHANGING the hours of a calendar that already exists must take effect (it did not: an upsert left the
	// column with a SQL default out of the update, so the first save won forever)
	changed := map[string]any{"3": "10:00-16:00", "0": "09:00-12:00"}
	code, raw = call(w.app.PutNextcloudCalendar, merge(base, map[string]any{"hours": changed}))
	require.Equal(t, 200, code, raw)
	_, raw = call(w.app.GetNextcloudCalendar, map[string]any{})
	assert.Contains(t, raw, `"3":"10:00-16:00"`, "the new hours")
	assert.Contains(t, raw, `"0":"09:00-12:00"`)
	assert.NotContains(t, raw, `"6":"08:00-12:00"`, "the old Saturday is gone")
	code, raw = call(w.app.PutNextcloudCalendar, merge(base, map[string]any{"hours": hours}))
	require.Equal(t, 200, code, raw)
	// and a save that does not mention the hours keeps them too
	code, raw = call(w.app.PutNextcloudCalendar, map[string]any{"location": "Centro"})
	require.Equal(t, 200, code, raw)
	_, raw = call(w.app.GetNextcloudCalendar, map[string]any{})
	assert.Contains(t, raw, `"6":"08:00-12:00"`)
	assert.Contains(t, raw, `"slot_minutes":45`)
}

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// --- the whole conversation, as the customer lives it ------------------------------------------------

// waTexts reads what the customer was sent, in order: the text of a message or the body and buttons of an
// interactive one. Typing indicators are not messages.
func (w *wiring) waTexts() []string {
	var out []string
	for _, b := range w.textSends() {
		var m struct {
			Text        *struct{ Body string } `json:"text"`
			Interactive *struct {
				Body   struct{ Text string } `json:"body"`
				Action struct {
					Buttons []struct {
						Reply struct{ ID, Title string } `json:"reply"`
					} `json:"buttons"`
				} `json:"action"`
			} `json:"interactive"`
		}
		if json.Unmarshal([]byte(b), &m) != nil {
			continue
		}
		switch {
		case m.Text != nil:
			out = append(out, m.Text.Body)
		case m.Interactive != nil:
			line := m.Interactive.Body.Text
			for _, btn := range m.Interactive.Action.Buttons {
				line += " [" + btn.Reply.Title + "]"
			}
			out = append(out, line)
		}
	}
	return out
}

// tap delivers the customer's tap on a button exactly as Meta's webhook spells it.
func (w *wiring) tap(t *testing.T, from, buttonID, title string) {
	t.Helper()
	w.inboundRaw(t, fmt.Sprintf(`{"from":%q,"id":"wamid.tap%s","type":"interactive","interactive":{"type":"button_reply","button_reply":{"id":%q,"title":%q}}}`,
		from, uuid.NewString()[:6], buttonID, title))
}

func TestScheduling_TheWholeConversationFromTheAdToTheBookedMeeting(t *testing.T) {
	const phone = "5571988887777"
	extraction := qualJSON(map[string]any{
		"area":   map[string]any{"valor": "Trabalhista", "evidencia": "ação trabalhista"},
		"cidade": map[string]any{"valor": "Salvador", "evidencia": "Moro em Salvador"},
	})
	// the agent's three model calls: a question, the handoff marker, and the extraction that follows it
	// the agent's model calls: a question, the handoff marker, the extraction that follows it, then (after the booking)
	// an answer about the meeting and the marker again when the customer asks to move it
	w := newWiring(t, "Olá! Sinto muito pela situação. Em qual cidade você mora?", "[[TRANSFERIR]]", extraction,
		"Sua conversa com o advogado está marcada, é só comparecer no horário combinado.", "[[TRANSFERIR]]")
	require.NoError(t, w.app.DB.Model(w.f.agent).Updates(map[string]any{
		"qualification_fields": lawFields, "objective": "Atender, qualificar e agendar uma conversa com o advogado",
		"company_info": "Escritório de advocacia. Nunca dê parecer jurídico.",
	}).Error)
	cal := newCalServer(t)
	w.withCalendar(t, cal)

	// 1. the customer arrives from the ad
	w.inboundRaw(t, fmt.Sprintf(`{"from":%q,"id":"wamid.c1","type":"text","text":{"body":"Oi, vi o anúncio. Fui demitida ontem"},`+
		`"referral":{"source_id":"AD-77","source_type":"ad","headline":"Consulta jurídica","ctwa_clid":"clk"}}`, phone))
	contact := w.contactFor(t, phone)
	require.NotNil(t, w.app.adOriginOf(w.f.org.ID, contact.ID), "the ad that brought them is on the contact")

	// 2. they answer the agent's question; the agent decides it has what it needs
	w.inboundRaw(t, fmt.Sprintf(`{"from":%q,"id":"wamid.c2","type":"text","text":{"body":"Moro em Salvador, quero entrar com uma ação trabalhista"}}`, phone))

	texts := w.waTexts()
	t.Log("conversation so far:\n  - " + strings.Join(texts, "\n  - "))
	require.GreaterOrEqual(t, len(texts), 2)
	assert.Contains(t, texts[len(texts)-2]+texts[len(texts)-1], "Em qual cidade", "the agent asked before offering")
	offer := texts[len(texts)-1]
	assert.Contains(t, offer, "escolha o melhor horário")
	assert.Len(t, w.offeredButtons(), 3)
	assert.NotContains(t, strings.Join(texts, " "), "TRANSFERIR", "the marker never reaches the customer")
	assert.Empty(t, w.transfers(t, contact.ID), "no attendant yet: the customer is choosing a time")
	opps := w.opportunities(t, contact.ID)
	require.Len(t, opps, 1)
	assert.Contains(t, opps[0].Interest, "Área do direito: Trabalhista")
	assert.Contains(t, opps[0].Interest, "Cidade: Salvador")
	assert.Zero(t, cal.putCount(), "nothing is booked until the customer picks")

	// 3. they tap the second time
	slots := w.offers(t, contact.ID)[0].Slots
	w.tap(t, phone, w.offeredButtons()[1], "horário 2")

	require.Equal(t, 1, cal.putCount(), "the tap reached the booking through the webhook path")
	ics := strings.ReplaceAll(cal.puts[0], "\r\n ", "")
	want, _ := time.Parse(time.RFC3339, slots[1])
	assert.Contains(t, ics, "DTSTART:"+want.UTC().Format("20060102T150405Z"))
	assert.Contains(t, ics, "Telefone: ")
	assert.Contains(t, ics, "Cidade: Salvador")
	assert.Contains(t, ics, "LOCATION:Escritório Centro")
	after := w.waTexts()
	t.Log("full conversation:\n  - " + strings.Join(after, "\n  - "))
	assert.Contains(t, after[len(after)-1], "Agendado!")
	assert.Contains(t, opps0(w, t, contact.ID).Interest, "Reunião agendada")
	assert.Empty(t, w.transfers(t, contact.ID), "the booking opens no attendance")
	ap := w.app.upcomingAppointment(w.f.org.ID, contact.ID)
	require.NotNil(t, ap)

	// 4. the customer keeps talking: the agent is still there, knows the meeting, and does not qualify or offer again
	w.inboundRaw(t, fmt.Sprintf(`{"from":%q,"id":"wamid.c3","type":"text","text":{"body":"Que horas é mesmo a conversa?"}}`, phone))
	texts4 := w.waTexts()
	assert.Contains(t, texts4[len(texts4)-1], "marcada", "the agent answered")
	lastAIBody := fmt.Sprint(w.ai.bodies[len(w.ai.bodies)-1])
	assert.Contains(t, lastAIBody, "Reunião já marcada", "the agent is told about the meeting")
	assert.Contains(t, lastAIBody, meetingLabel(ap.Local))
	assert.Empty(t, w.transfers(t, contact.ID))
	assert.Equal(t, 1, cal.putCount())

	// 5. it asks to move it: the agent calls people, and never offers a second time
	w.inboundRaw(t, fmt.Sprintf(`{"from":%q,"id":"wamid.c4","type":"text","text":{"body":"Preciso remarcar para outro dia"}}`, phone))
	final := w.waTexts()
	t.Log("conversation after the booking:\n  - " + strings.Join(final[len(after):], "\n  - "))
	require.Len(t, w.transfers(t, contact.ID), 1, "people take a reschedule")
	assert.Len(t, w.offeredButtons(), 3, "no new offer: still only the first three buttons")
	assert.Equal(t, 1, cal.putCount(), "never a second meeting")
	assert.Contains(t, w.transfers(t, contact.ID)[0].Notes, "reunião já marcada", "the person sees the meeting")
	assert.Contains(t, final[len(final)-1], "atendente")
}

func opps0(w *wiring, t *testing.T, contactID uuid.UUID) models.SalesOpportunity {
	t.Helper()
	o := w.opportunities(t, contactID)
	require.NotEmpty(t, o)
	return o[0]
}

func TestUpcomingAppointment_OnlyTheCustomersOwnFutureBookedMeeting(t *testing.T) {
	w := newWiring(t, "x")
	contact, session := w.handoffSession(t, "Oi")
	other, _ := w.handoffSession(t, "Oi")
	mk := func(c *models.Contact, status string, start time.Time) {
		st := start
		require.NoError(t, w.app.DB.Create(&models.AIAppointmentOffer{
			BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID, ContactID: c.ID, SessionID: session.ID,
			WhatsAppAccount: w.f.account.Name, Token: uuid.NewString(), Status: status, ExpiresAt: time.Now().Add(time.Hour), BookedStart: &st,
		}).Error)
	}
	assert.Nil(t, w.app.upcomingAppointment(w.f.org.ID, contact.ID), "nothing booked")

	mk(contact, models.AIAppointmentBooked, time.Now().Add(-48*time.Hour)) // already happened
	mk(contact, models.AIAppointmentExpired, time.Now().Add(48*time.Hour)) // never booked
	mk(other, models.AIAppointmentBooked, time.Now().Add(48*time.Hour))    // somebody else's
	assert.Nil(t, w.app.upcomingAppointment(w.f.org.ID, contact.ID))

	next := time.Now().Add(72 * time.Hour)
	mk(contact, models.AIAppointmentBooked, next)
	mk(contact, models.AIAppointmentBooked, next.Add(48*time.Hour))
	got := w.app.upcomingAppointment(w.f.org.ID, contact.ID)
	require.NotNil(t, got)
	assert.WithinDuration(t, next, got.Start, time.Second, "the next one")
}

func TestScheduling_ACustomerWhoAlreadyHasAMeetingIsNeverOfferedASecondOne(t *testing.T) {
	w, cal, contact, session := newScheduler(t)
	start := time.Now().Add(48 * time.Hour)
	require.NoError(t, w.app.DB.Create(&models.AIAppointmentOffer{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID, ContactID: contact.ID, SessionID: session.ID,
		WhatsAppAccount: w.f.account.Name, Token: uuid.NewString(), Status: models.AIAppointmentBooked,
		ExpiresAt: time.Now().Add(time.Hour), BookedStart: &start, EventUID: "x@whatc",
	}).Error)

	out := w.app.handoffFromAIOutcome(w.f.account, contact, session, w.f.settings, aiHandoffMarker)

	assert.Equal(t, aiHandoffTransferred, out, "people take it")
	assert.Empty(t, w.offeredButtons(), "no new buttons")
	assert.Zero(t, cal.putCount())
	ts := w.transfers(t, contact.ID)
	require.Len(t, ts, 1)
	assert.Contains(t, ts[0].Notes, "reunião já marcada")
}

func TestAppointmentPromptSection_TellsTheAgentTheMeetingAndWhenToCallPeople(t *testing.T) {
	zone, _ := time.LoadLocation("America/Sao_Paulo")
	ap := &upcomingAppointment{Local: time.Date(2026, 11, 24, 9, 0, 0, 0, zone), Location: "Escritório Centro"}
	got := appointmentPromptSection(ap)
	assert.Contains(t, got, "ter, 24/11 às 09:00")
	assert.Contains(t, got, "Escritório Centro")
	assert.Contains(t, got, "NUNCA ofereça ou combine outros horários")
	assert.Contains(t, got, "[[TRANSFERIR]]")
	assert.Contains(t, got, "remarcar")
	assert.Contains(t, got, "sobre o caso dele", "a question about the case is for the lawyer, not the agent")
}
