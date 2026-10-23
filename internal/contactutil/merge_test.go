package contactutil

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeDuplicateContacts(t *testing.T) {
	db := testutil.SetupTestDB(t)
	uid := uuid.New().String()[:8]
	org := models.Organization{BaseModel: models.BaseModel{ID: uuid.New()}, Name: "m-" + uid, Slug: "m-" + uid}
	require.NoError(t, db.Create(&org).Error)

	mk := func(phone string, age time.Duration) models.Contact {
		c := models.Contact{BaseModel: models.BaseModel{ID: uuid.New(), CreatedAt: time.Now().Add(-age)}, OrganizationID: org.ID, PhoneNumber: phone}
		require.NoError(t, db.Create(&c).Error)
		return c
	}
	oldest := mk("71996002928", 3*time.Hour) // saved without the country code
	mid := mk("5571996002928", 2*time.Hour)
	last := mk("557196002928", time.Hour) // legacy 8-digit form
	other := mk("5511988887777", time.Hour)

	author := testutil.CreateTestUser(t, db, org.ID)
	note := models.ConversationNote{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: last.ID, CreatedByID: author.ID, Content: "n"}
	require.NoError(t, db.Create(&note).Error)

	rep, err := MergeDuplicateContacts(db, org.ID, false)
	require.NoError(t, err)
	assert.Equal(t, 1, rep.Groups)
	assert.Equal(t, 2, rep.Duplicates)
	var n int64
	db.Model(&models.Contact{}).Where("organization_id = ?", org.ID).Count(&n)
	assert.EqualValues(t, 4, n, "dry run writes nothing")

	rep, err = MergeDuplicateContacts(db, org.ID, true)
	require.NoError(t, err)
	assert.Empty(t, rep.Skipped)

	var live []models.Contact
	require.NoError(t, db.Where("organization_id = ?", org.ID).Find(&live).Error)
	assert.Len(t, live, 2)
	var kept models.Contact
	require.NoError(t, db.First(&kept, oldest.ID).Error)
	assert.Equal(t, "5571996002928", kept.PhoneNumber)
	var moved models.ConversationNote
	require.NoError(t, db.First(&moved, note.ID).Error)
	assert.Equal(t, oldest.ID, moved.ContactID)
	assert.NoError(t, db.First(&models.Contact{}, other.ID).Error)
	_ = mid
}
