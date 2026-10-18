package handlers_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// Spreadsheet formula injection in exported CSV (csv_safe.go).

// Cells a spreadsheet would evaluate: each must come out behind an apostrophe.
var csvFormulaVectors = []string{
	"=1+1", "=cmd|' /C calc'!A0", "=HYPERLINK(\"http://evil\",\"x\")", "+cmd|' /C calc'!A0", "-2+3+cmd|' /C calc'!A0",
	"@SUM(A1:A2)", "=", "@", "+", "-", "--5", "- Joana", "+ Café",
	"\t=1", "\r=1", "\n=1", "\t\t-1", "\t5", // TAB and CR are triggers on their own
	" =1+1", "   @x", "  \t+1", "\u00a0=1", "\u2003=1", // spaces (and NBSP/em-space) in front do not hide the trigger
	"\x01=1", "\x7f@x", // control characters in front
	"\u200b=1", "\ufeff=1", "\u202e=1", "\u2060+1", // invisible / bidi characters in front
	"''=1", // already starts with apostrophes, but a trigger follows them
}

// Legitimate values: unchanged.
var csvLegitValues = []string{
	"", "Maria", "José da Silva", "O'Neil", "'quoted", "''quoted", "'5", "'-5",
	"5511999998888", "+5511999998888", "-5", "+1", "1.5", "-1,5", "+55", // numbers keep their representation
	"2026-10-18T10:00:00Z", "#ff0000", "a=b", "user@example.com", "x+y", "1-2", "(=1)", "ok,fine", "tag one,tag two",
}

func TestCSVSafeCell_NeutralizesEveryFormulaVector(t *testing.T) {
	for _, v := range csvFormulaVectors {
		got := handlers.CSVSafeCellForTest(v)
		assert.Equal(t, "'"+v, got, "guarded: %q", v)
		assert.False(t, handlers.CSVLooksLikeFormulaForTest(got), "no longer a formula: %q", got)
	}
}

func TestCSVSafeCell_LeavesLegitimateValuesAlone(t *testing.T) {
	for _, v := range csvLegitValues {
		assert.Equal(t, v, handlers.CSVSafeCellForTest(v), "unchanged: %q", v)
	}
}

func TestCSVCell_RoundTripIsExact(t *testing.T) {
	all := append(append([]string{}, csvFormulaVectors...), csvLegitValues...)
	for _, v := range all {
		assert.Equal(t, v, handlers.CSVUnsafeCellForTest(handlers.CSVSafeCellForTest(v)), "export then import gives back %q", v)
	}
	// Data that merely looks like the guard is not touched on import.
	for _, v := range []string{"'hello", "'5", "'-5", "''abc", "'", "'a=1"} {
		assert.Equal(t, v, handlers.CSVUnsafeCellForTest(v), "not a guard: %q", v)
	}
	// The apostrophe the export adds is the one removed.
	assert.Equal(t, "=1+1", handlers.CSVUnsafeCellForTest("'=1+1"))
	assert.Equal(t, "'=1", handlers.CSVUnsafeCellForTest("''=1"))
}

// Every string up to four characters over an alphabet of triggers, spaces, apostrophes and
// plain characters: nothing the export writes is ever a formula, and import always undoes it.
func TestCSVCell_ExhaustiveSmallStrings(t *testing.T) {
	alphabet := []string{"=", "+", "-", "@", "\t", "\r", " ", "'", "a", "1", "\u200b", "\x01"}
	var gen func(prefix string, n int)
	count := 0
	gen = func(prefix string, n int) {
		safe := handlers.CSVSafeCellForTest(prefix)
		if handlers.CSVLooksLikeFormulaForTest(safe) {
			t.Fatalf("export of %q is still a formula: %q", prefix, safe)
		}
		if back := handlers.CSVUnsafeCellForTest(safe); back != prefix {
			t.Fatalf("round trip of %q gave %q", prefix, back)
		}
		count++
		if n == 0 {
			return
		}
		for _, a := range alphabet {
			gen(prefix+a, n-1)
		}
	}
	gen("", 4)
	assert.Greater(t, count, 20000)
}

// --- end to end through the real endpoints ---

func exportCSV(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, table string, columns []string) [][]string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"table": table, "columns": columns})
	req := testutil.NewRequest(t)
	req.RequestCtx.Request.Header.SetContentType("application/json")
	req.RequestCtx.Request.Header.SetMethod("POST")
	req.RequestCtx.Request.SetBody(body)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.ExportData(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
	rows, err := csv.NewReader(bytes.NewReader(testutil.GetResponseBody(req))).ReadAll()
	require.NoError(t, err)
	return rows
}

func importCSV(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, table, content string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	require.NoError(t, mw.WriteField("table", table))
	fw, err := mw.CreateFormFile("file", "import.csv")
	require.NoError(t, err)
	_, _ = fw.Write([]byte(content))
	require.NoError(t, mw.Close())
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.SetContentType(mw.FormDataContentType())
	ctx.Request.SetBody(buf.Bytes())
	req := &fastglue.Request{RequestCtx: ctx}
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.ImportData(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
}

func TestApp_ExportData_EveryTextCellIsNeutralized(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := contactsExportRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	evil := "=HYPERLINK(\"http://evil\",\"click\")"
	contacts := []models.Contact{
		{PhoneNumber: "5511999990001", ProfileName: evil, WhatsAppAccount: "+cmd|' /C calc'!A0"},
		{PhoneNumber: "5511999990002", ProfileName: "\t=1", WhatsAppAccount: " @x"},
		{PhoneNumber: "5511999990003", ProfileName: "Maria", WhatsAppAccount: "main"},
		{PhoneNumber: "5511999990004", ProfileName: "+ Café", WhatsAppAccount: "main"},
	}
	for i := range contacts {
		contacts[i].BaseModel = models.BaseModel{ID: uuid.New()}
		contacts[i].OrganizationID = org.ID
		require.NoError(t, app.DB.Create(&contacts[i]).Error)
	}

	rows := exportCSV(t, app, org.ID, user.ID, "contacts", []string{"phone_number", "profile_name", "whats_app_account", "created_at"})
	require.Equal(t, []string{"Phone Number", "Name", "WhatsApp Account", "Created At"}, rows[0], "headers are untouched")

	byPhone := map[string][]string{}
	for _, row := range rows[1:] {
		byPhone[row[0]] = row
	}
	cases := map[string][]string{ // phone -> expected name, account
		"5511999990001": {"'" + evil, "'+cmd|' /C calc'!A0"},
		"5511999990002": {"'\t=1", "' @x"},
		"5511999990003": {"Maria", "main"}, // legitimate: unchanged
		"5511999990004": {"'+ Café", "main"},
	}
	for phone, want := range cases {
		row := byPhone[phone]
		require.NotNil(t, row, phone)
		assert.Equal(t, want[0], row[1], "profile_name of %s", phone)
		assert.Equal(t, want[1], row[2], "whats_app_account of %s", phone)
		assert.Equal(t, phone, row[0], "the phone number keeps its representation")
		assert.NotEmpty(t, row[3])
		for _, cell := range row {
			assert.False(t, handlers.CSVLooksLikeFormulaForTest(cell), "no cell of the file is a formula: %q", cell)
		}
	}
}

// Export, edit nothing, import elsewhere: the data comes back as it was, the guard does not stick.
func TestApp_ExportImportRoundTrip_DoesNotKeepTheApostrophe(t *testing.T) {
	app := newTestApp(t)
	src := testutil.CreateTestOrganization(t, app.DB)
	dst := testutil.CreateTestOrganization(t, app.DB)
	role1 := contactsExportRole(t, app.DB, src.ID)
	role2 := contactsExportRole(t, app.DB, dst.ID)
	u1 := testutil.CreateTestUser(t, app.DB, src.ID, testutil.WithRoleID(&role1.ID))
	u2 := testutil.CreateTestUser(t, app.DB, dst.ID, testutil.WithRoleID(&role2.ID))

	names := map[string]string{ // phone -> profile name
		"5511888880001": "=HYPERLINK(\"http://evil\",\"x\")",
		"5511888880002": "+cmd|' /C calc'!A0",
		"5511888880003": "'=already quoted",
		"5511888880004": "'hello", // data that merely starts with an apostrophe
		"5511888880005": "Maria",
		"5511888880006": "@someone",
	}
	for phone, name := range names {
		require.NoError(t, app.DB.Create(&models.Contact{
			BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: src.ID, PhoneNumber: phone, ProfileName: name,
		}).Error)
	}

	rows := exportCSV(t, app, src.ID, u1.ID, "contacts", []string{"phone_number", "profile_name"})
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	require.NoError(t, w.WriteAll(rows)) // the file exactly as exported
	importCSV(t, app, dst.ID, u2.ID, "contacts", out.String())

	for phone, want := range names {
		var c models.Contact
		require.NoError(t, app.DB.Where("organization_id = ? AND phone_number = ?", dst.ID, phone).First(&c).Error, phone)
		assert.Equal(t, want, c.ProfileName, "imported %s", phone)
	}
}

// What a person typed into a spreadsheet is imported as written, apostrophes included.
func TestApp_ImportData_LeavesOrdinaryApostrophesAlone(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := contactsExportRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	content := fmt.Sprintf("phone_number,profile_name\n5511777770001,%s\n5511777770002,%s\n5511777770003,%s\n",
		"O'Neil", "'quoted", "'=guarded")
	importCSV(t, app, org.ID, user.ID, "contacts", content)
	want := map[string]string{"5511777770001": "O'Neil", "5511777770002": "'quoted", "5511777770003": "=guarded"}
	for phone, name := range want {
		var c models.Contact
		require.NoError(t, app.DB.Where("organization_id = ? AND phone_number = ?", org.ID, phone).First(&c).Error)
		assert.Equal(t, name, c.ProfileName, phone)
	}
}
