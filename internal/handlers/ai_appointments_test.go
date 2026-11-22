package handlers

import (
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

func TestScheduling_TappingATimeBooksItAndHandsTheCustomerToPeople(t *testing.T) {
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
	assert.Len(t, w.transfers(t, contact.ID), 1, "people take the attendance after the booking")
	assert.Equal(t, 0, countContaining(w.textSends(), aiHandoffWaitMessage[:20]), "the booking message is the only promise")
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
	assert.Len(t, w.transfers(t, contact.ID), 1)
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
