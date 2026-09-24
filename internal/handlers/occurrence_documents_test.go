package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestOccurrenceDocuments_CreateWithProtocolAndAttach(t *testing.T) {
	app := newTestApp(t)
	app.Config.Storage.LocalPath = t.TempDir()
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	// A customer with an NF from one purchase and only the cupom from another.
	req := createOccurrenceWith(t, app, org.ID, user.ID, contact.ID, map[string]any{
		"documents": []map[string]any{
			{"type": "nf", "number": "12345", "purchase_date": "2026-09-10", "items": []map[string]any{
				{"code": "PL01", "description": "Piso Laminado Carvalho", "quantity": "20 m²"},
				{"description": "  "}, // blank line from the "add product" button: dropped
			}},
			{"type": "cupom", "number": "PED-778", "purchase_date": "2026-09-02", "items": []map[string]any{
				{"description": "Rodapé MDF", "quantity": "4 un"},
			}},
		},
	})
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("organization_id = ? AND contact_id = ?", org.ID, contact.ID).First(&occ).Error)
	assert.Equal(t, "12345, PED-778", occ.InvoiceNumber, "legacy field keeps [NF] templates working")
	assert.Equal(t, "Piso Laminado Carvalho; Rodapé MDF", occ.ProductDescription)
	require.NotNil(t, occ.PurchaseDate)
	assert.Equal(t, "2026-09-02", occ.PurchaseDate.Format("2006-01-02"), "earliest purchase")

	var docs []models.OccurrenceDocument
	require.NoError(t, app.DB.Where("occurrence_id = ?", occ.ID).Order("created_at, number").Find(&docs).Error)
	require.Len(t, docs, 2)
	var nf models.OccurrenceDocument
	for _, d := range docs {
		if d.Type == models.OccurrenceDocumentNF {
			nf = d
		}
	}
	assert.Len(t, nf.Items, 1)

	// Attach the NF PDF, then read it back.
	pdf := []byte("%PDF-1.4\n1 0 obj << >> endobj\ntrailer << >>\n%%EOF")
	up := newMultipartUploadRequestWithContentType(t, "file", "nota.pdf", pdf, "application/pdf")
	testutil.SetAuthContext(up, org.ID, user.ID)
	testutil.SetPathParam(up, "id", occ.ID.String())
	testutil.SetPathParam(up, "docId", nf.ID.String())
	require.NoError(t, app.UploadOccurrenceDocumentAttachment(up))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(up))

	get := testutil.NewGETRequest(t)
	testutil.SetAuthContext(get, org.ID, user.ID)
	testutil.SetPathParam(get, "id", occ.ID.String())
	testutil.SetPathParam(get, "docId", nf.ID.String())
	require.NoError(t, app.ServeOccurrenceDocumentAttachment(get))
	assert.Equal(t, pdf, testutil.GetResponseBody(get))
	assert.Equal(t, "application/pdf", string(get.RequestCtx.Response.Header.ContentType()))

	// A text file dressed up as a PDF is refused.
	bad := newMultipartUploadRequestWithContentType(t, "file", "nota.pdf", []byte("not a pdf"), "application/pdf")
	testutil.SetAuthContext(bad, org.ID, user.ID)
	testutil.SetPathParam(bad, "id", occ.ID.String())
	testutil.SetPathParam(bad, "docId", nf.ID.String())
	require.NoError(t, app.UploadOccurrenceDocumentAttachment(bad))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(bad))

	// Listing returns both documents with the attachment name, never the path.
	list := testutil.NewGETRequest(t)
	testutil.SetAuthContext(list, org.ID, user.ID)
	testutil.SetPathParam(list, "id", occ.ID.String())
	require.NoError(t, app.ListOccurrenceDocuments(list))
	var body struct {
		Data struct {
			Documents []map[string]any `json:"documents"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(list), &body))
	require.Len(t, body.Data.Documents, 2)
	for _, d := range body.Data.Documents {
		assert.NotContains(t, d, "attachment_path")
	}
}

func TestOccurrenceDocuments_RejectsUnknownType(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	req := createOccurrenceWith(t, app, org.ID, user.ID, contact.ID, map[string]any{
		"documents": []map[string]any{{"type": "boleto", "number": "1"}},
	})
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))

	var count int64
	app.DB.Model(&models.Occurrence{}).Where("organization_id = ?", org.ID).Count(&count)
	assert.Zero(t, count, "an invalid document must not leave a half-created protocol")
}
