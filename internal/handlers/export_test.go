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
