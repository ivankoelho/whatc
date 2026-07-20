package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// TransitionContactStatusForTest exposes transitionContactStatus to the
// external handlers_test package.
func (a *App) TransitionContactStatusForTest(
	contact *models.Contact,
	to models.ContactStatus,
	from []models.ContactStatus,
	actorID *uuid.UUID,
) (bool, error) {
	return a.transitionContactStatus(contact, to, from, actorID)
}

// SenderNameForBroadcastForTest exposes senderNameForBroadcast to the external
// handlers_test package, so its result can be pinned against senderName() —
// the REST-side producer of the same sent_by_user_name field.
func (a *App) SenderNameForBroadcastForTest(msg *models.Message) string {
	return a.senderNameForBroadcast(msg)
}

// SenderNameForTest exposes senderName to the external handlers_test package.
func SenderNameForTest(m *models.Message) string {
	return senderName(m)
}
