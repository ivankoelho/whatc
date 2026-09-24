package handlers_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
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

// docRequest builds a request against /occurrences/{id}/documents[/{docId}].
func docRequest(t *testing.T, body any, orgID, userID, occID, docID uuid.UUID) *fastglue.Request {
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	testutil.SetPathParam(req, "id", occID.String())
	if docID != uuid.Nil {
		testutil.SetPathParam(req, "docId", docID.String())
	}
	return req
}

func TestOccurrenceDocuments_ProductWithoutNumberIsAccepted(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	// Same as the old form: a product with no NF number.
	req := createOccurrenceWith(t, app, org.ID, user.ID, contact.ID, map[string]any{
		"documents": []map[string]any{{"type": "nf", "items": []map[string]any{{"description": "Porcelanato 60x60"}}}},
	})
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	assert.Equal(t, "Porcelanato 60x60", occ.ProductDescription)
	assert.Equal(t, "Porcelanato 60x60", occ.Title, "title still derives from the product")
	assert.Empty(t, occ.InvoiceNumber)
}

func TestOccurrenceDocuments_AddAndDeleteLaterKeepSummaryAndLeaveNoOrphans(t *testing.T) {
	app := newTestApp(t)
	app.Config.Storage.LocalPath = t.TempDir()
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID), testutil.WithSuperAdmin())
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	// A protocol opened the old way: NF typed in the single field, no documents.
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(createOccurrenceWith(t, app, org.ID, user.ID, contact.ID,
		map[string]any{"invoice_number": "OLD-1", "product_description": "Piso antigo"})))
	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)

	add := func(body map[string]any) models.OccurrenceDocument {
		req := docRequest(t, body, org.ID, user.ID, occ.ID, uuid.Nil)
		require.NoError(t, app.CreateOccurrenceDocument(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
		var resp struct{ Data models.OccurrenceDocument }
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		return resp.Data
	}
	reload := func() models.Occurrence {
		var o models.Occurrence
		require.NoError(t, app.DB.First(&o, "id = ?", occ.ID).Error)
		return o
	}
	upload := func(doc models.OccurrenceDocument, content []byte) {
		up := newMultipartUploadRequestWithContentType(t, "file", "f.pdf", content, "application/pdf")
		testutil.SetAuthContext(up, org.ID, user.ID)
		testutil.SetPathParam(up, "id", occ.ID.String())
		testutil.SetPathParam(up, "docId", doc.ID.String())
		require.NoError(t, app.UploadOccurrenceDocumentAttachment(up))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(up))
	}

	nf := add(map[string]any{"type": "nf", "number": "NF-2", "purchase_date": "2026-09-05", "items": []map[string]any{{"description": "Rodapé"}}})
	cupom := add(map[string]any{"type": "cupom", "number": "PED-3", "items": []map[string]any{{"description": "Argamassa"}}})
	o := reload()
	assert.Equal(t, "OLD-1, NF-2, PED-3", o.InvoiceNumber, "value typed before documents is kept")
	assert.Equal(t, "Piso antigo; Rodapé; Argamassa", o.ProductDescription)
	require.NotNil(t, o.PurchaseDate)

	// Each document keeps its own file.
	nfPDF := []byte("%PDF-1.4 nf\n%%EOF")
	cupomPDF := []byte("%PDF-1.4 cupom\n%%EOF")
	upload(nf, nfPDF)
	upload(cupom, cupomPDF)
	get := docRequest(t, nil, org.ID, user.ID, occ.ID, cupom.ID)
	require.NoError(t, app.ServeOccurrenceDocumentAttachment(get))
	assert.Equal(t, cupomPDF, testutil.GetResponseBody(get))

	// Deleting the NF removes its row, its file and its part of the summary.
	var nfRow models.OccurrenceDocument
	require.NoError(t, app.DB.First(&nfRow, "id = ?", nf.ID).Error)
	nfFile := filepath.Join(app.Config.Storage.LocalPath, nfRow.AttachmentPath)
	require.FileExists(t, nfFile)
	del := docRequest(t, nil, org.ID, user.ID, occ.ID, nf.ID)
	require.NoError(t, app.DeleteOccurrenceDocument(del))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(del))
	assert.NoFileExists(t, nfFile)
	var count int64
	app.DB.Unscoped().Model(&models.OccurrenceDocument{}).Where("id = ?", nf.ID).Count(&count)
	assert.Zero(t, count, "no soft-deleted row pointing at a removed file")
	o = reload()
	assert.Equal(t, "OLD-1, PED-3", o.InvoiceNumber)
	assert.Equal(t, "Piso antigo; Argamassa", o.ProductDescription)
	assert.Nil(t, o.PurchaseDate, "the only dated document is gone")

	// Deleting the protocol takes the remaining documents and files with it.
	var cupomRow models.OccurrenceDocument
	require.NoError(t, app.DB.First(&cupomRow, "id = ?", cupom.ID).Error)
	cupomFile := filepath.Join(app.Config.Storage.LocalPath, cupomRow.AttachmentPath)
	delOcc := testutil.NewRequest(t)
	testutil.SetAuthContext(delOcc, org.ID, user.ID)
	testutil.SetPathParam(delOcc, "id", occ.ID.String())
	require.NoError(t, app.DeleteOccurrence(delOcc))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(delOcc))
	app.DB.Unscoped().Model(&models.OccurrenceDocument{}).Where("occurrence_id = ?", occ.ID).Count(&count)
	assert.Zero(t, count)
	assert.NoFileExists(t, cupomFile)
}

func TestOccurrenceDocuments_AttachmentOver10MBIsRejected(t *testing.T) {
	app := newTestApp(t)
	app.Config.Storage.LocalPath = t.TempDir()
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(createOccurrenceWith(t, app, org.ID, user.ID, contact.ID,
		map[string]any{"documents": []map[string]any{{"type": "nf", "number": "1"}}})))
	var doc models.OccurrenceDocument
	require.NoError(t, app.DB.Joins("JOIN occurrences o ON o.id = occurrence_documents.occurrence_id").
		Where("o.contact_id = ?", contact.ID).First(&doc).Error)

	big := append([]byte("%PDF-1.4\n"), make([]byte, 10<<20)...)
	up := newMultipartUploadRequestWithContentType(t, "file", "grande.pdf", big, "application/pdf")
	testutil.SetAuthContext(up, org.ID, user.ID)
	testutil.SetPathParam(up, "id", doc.OccurrenceID.String())
	testutil.SetPathParam(up, "docId", doc.ID.String())
	require.NoError(t, app.UploadOccurrenceDocumentAttachment(up))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(up))
	require.NoError(t, app.DB.First(&doc, "id = ?", doc.ID).Error)
	assert.Empty(t, doc.AttachmentPath)
}
