package handlers

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm/clause"
)

// recordMetaMedia notes a media id this server uploaded to Meta from bytes it had just scanned clean.
func (a *App) recordMetaMedia(orgID uuid.UUID, account, mediaID, source string, data []byte) {
	if mediaID == "" {
		return
	}
	sum := sha256.Sum256(data)
	err := a.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&models.MetaMedia{
		ID: uuid.New(), OrganizationID: orgID, MediaID: mediaID, Account: account, Source: source, SHA256: hex.EncodeToString(sum[:]),
	}).Error
	if err != nil {
		a.Log.Error("Could not record an uploaded media id", "error", err)
	}
}

// metaMediaKnown: the id was uploaded by this server for this organization (after a scan).
func (a *App) metaMediaKnown(orgID uuid.UUID, mediaID string) bool {
	var n int64
	a.DB.Model(&models.MetaMedia{}).Where("organization_id = ? AND media_id = ?", orgID, mediaID).Count(&n)
	return n > 0
}
