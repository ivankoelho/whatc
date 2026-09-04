package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// Test-only alias. resolveWhatsAppAccountByName stays unexported; this
// exists so package-external tests can drive it without widening the real API.
func (a *App) ResolveWhatsAppAccountByNameForTest(orgID uuid.UUID, name string) (*models.WhatsAppAccount, error) {
	return a.resolveWhatsAppAccountByName(orgID, name)
}
