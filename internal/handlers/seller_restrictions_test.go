package handlers_test

import (
	"bytes"
	"mime/multipart"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// A seller profile can work existing conversations but holds no contacts:write
// / contacts:import, so no endpoint may create a contact for it.
// The keys are the real "Vendas" preset, not a hand-made list, so changing the
// preset changes what these tests prove.
var sellerKeys = func() []string {
	for _, p := range models.TeamProfiles() {
		if p.Key == "vendas" {
			return p.Permissions
		}
	}
	panic("vendas preset missing")
}()

func TestSeller_CannotCreateContactsByAnyRoute(t *testing.T) {
	t.Parallel()
	mock := newMockWhatsAppServer()
	defer mock.close()
	app := newMsgTestApp(t, mock)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "seller", sellerKeys)
	seller := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	account := createTestAccount(t, app, org.ID)
	tpl := createTestTemplate(t, app, org.ID, account.Name)

	contacts := func() int64 {
		var n int64
		app.DB.Unscoped().Model(&models.Contact{}).Where("organization_id = ?", org.ID).Count(&n)
		return n
	}

	t.Run("POST /contacts", func(t *testing.T) {
		req := testutil.NewJSONRequest(t, map[string]any{"phone_number": "5511999990001"})
		testutil.SetAuthContext(req, org.ID, seller.ID)
		require.NoError(t, app.CreateContact(req))
		assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
		assert.Zero(t, contacts())
	})

	t.Run("template send with unknown phone_number (all spellings)", func(t *testing.T) {
		for _, phone := range []string{"5511999990002", "+5511999990002", "55 (11) 99999-0002"} {
			req := testutil.NewJSONRequest(t, map[string]any{
				"phone_number": phone, "template_name": tpl.Name, "account_name": account.Name,
				"template_params": map[string]string{"name": "X", "order_id": "1"},
			})
			testutil.SetAuthContext(req, org.ID, seller.ID)
			require.NoError(t, app.SendTemplateMessage(req))
			assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req), phone)
		}
		assert.Zero(t, contacts(), "no contact may be created implicitly")
	})

	t.Run("import contacts", func(t *testing.T) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		_ = w.WriteField("table", "contacts")
		fw, _ := w.CreateFormFile("file", "c.csv")
		_, _ = fw.Write([]byte("phone_number\n5511999990003\n"))
		_ = w.Close()
		req := testutil.NewRequest(t)
		req.RequestCtx.Request.Header.SetMethod("POST")
		req.RequestCtx.Request.Header.SetContentType(w.FormDataContentType())
		req.RequestCtx.Request.SetBody(buf.Bytes())
		testutil.SetAuthContext(req, org.ID, seller.ID)
		require.NoError(t, app.ImportData(req))
		assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
		assert.Zero(t, contacts())
	})
}

func TestSeller_CanStillWorkExistingContacts(t *testing.T) {
	t.Parallel()
	mock := newMockWhatsAppServer()
	defer mock.close()
	app := newMsgTestApp(t, mock)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "seller", sellerKeys)
	seller := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	account := createTestAccount(t, app, org.ID)
	tpl := createTestTemplate(t, app, org.ID, account.Name)
	existing := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithPhoneNumber("5511999990009"))
	// "Existing and authorized": the contact is in the seller's carteira.
	require.NoError(t, app.DB.Model(existing).Update("assigned_user_id", seller.ID).Error)

	// Existing phone: not blocked by the new-contact gate.
	req := testutil.NewJSONRequest(t, map[string]any{
		"phone_number": existing.PhoneNumber, "template_name": tpl.Name, "account_name": account.Name,
		"template_params": map[string]string{"name": "X", "order_id": "1"},
	})
	testutil.SetAuthContext(req, org.ID, seller.ID)
	require.NoError(t, app.SendTemplateMessage(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	// And by contact_id.
	req = testutil.NewJSONRequest(t, map[string]any{
		"contact_id": existing.ID.String(), "template_name": tpl.Name, "account_name": account.Name,
		"template_params": map[string]string{"name": "X", "order_id": "1"},
	})
	testutil.SetAuthContext(req, org.ID, seller.ID)
	require.NoError(t, app.SendTemplateMessage(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
}
