package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/xprocess"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// xprocessIntegrationResponse never carries APIKey — see the model's own
// json:"-" tag, this is the second, explicit layer (design §4).
type xprocessIntegrationResponse struct {
	BaseURL      string `json:"base_url"`
	IsActive     bool   `json:"is_active"`
	IsConfigured bool   `json:"is_configured"`
}

// GetXProcessIntegration returns the organization's X2 credential status —
// never the key itself.
func (a *App) GetXProcessIntegration(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceXProcessIntegration, models.ActionRead)
	if err != nil {
		return nil
	}
	var integ models.XProcessIntegration
	if err := a.DB.Where("organization_id = ?", orgID).First(&integ).Error; err != nil {
		return r.SendEnvelope(xprocessIntegrationResponse{})
	}
	return r.SendEnvelope(xprocessIntegrationResponse{
		BaseURL: integ.BaseURL, IsActive: integ.IsActive, IsConfigured: true,
	})
}

type upsertXProcessIntegrationRequest struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// UpsertXProcessIntegration creates or replaces the organization's single
// X2 credential row (design §4 — one per organization).
func (a *App) UpsertXProcessIntegration(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceXProcessIntegration, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req upsertXProcessIntegrationRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.BaseURL == "" || req.APIKey == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "base_url and api_key are required", nil, "")
	}

	var integ models.XProcessIntegration
	found := a.DB.Where("organization_id = ?", orgID).First(&integ).Error == nil
	integ.OrganizationID = orgID
	integ.BaseURL = req.BaseURL
	integ.APIKey = req.APIKey
	integ.IsActive = true
	if err := integ.EncryptSecrets(a.Config.App.EncryptionKey); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to secure the credential", nil, "")
	}

	if found {
		if err := a.DB.Model(&models.XProcessIntegration{}).
			Where("organization_id = ?", orgID).
			Updates(map[string]any{"base_url": integ.BaseURL, "api_key": integ.APIKey, "is_active": true}).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update integration", nil, "")
		}
	} else if err := a.DB.Create(&integ).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create integration", nil, "")
	}

	return r.SendEnvelope(xprocessIntegrationResponse{BaseURL: req.BaseURL, IsActive: true, IsConfigured: true})
}

type testXProcessIntegrationRequest struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// TestXProcessIntegrationConnection validates a base_url/api_key pair
// against the live X2 API without saving anything — lets the admin catch a
// typo before committing the credential (design §B in the settings tab
// decision). Checks /liveness (no auth) then POST /api/pedido (with the
// key, via the same xprocess.Client the reconciliation job uses) so both
// "wrong URL" and "wrong key" are distinguished by the error.
func (a *App) TestXProcessIntegrationConnection(r *fastglue.Request) error {
	_, _, err := a.requireAuth(r, models.ResourceXProcessIntegration, models.ActionWrite)
	if err != nil {
		return nil
	}
	var req testXProcessIntegrationRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.BaseURL == "" || req.APIKey == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "base_url and api_key are required", nil, "")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	livenessReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.BaseURL+"/liveness", nil)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Could not reach base_url", nil, "")
	}
	livenessResp, err := a.HTTPClient.Do(livenessReq)
	if err != nil || livenessResp.StatusCode >= 300 {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Could not reach base_url", nil, "")
	}
	_ = livenessResp.Body.Close()

	client := xprocess.New(a.Log, req.BaseURL)
	// POST /api/pedido with a deliberately-fake pedido/documento — a cheap,
	// side-effect-free way to prove the key authenticates without needing a
	// real pedido.
	if _, err := client.ConsultarPedido(ctx, req.APIKey, "__connection_test__", "00000000000"); err != nil {
		// ErrPedidoNaoEncontrado on this deliberately-fake pedido actually
		// PROVES the key works (X2 validated the key before it could even
		// say "not found") — anything else is a real connectivity/auth failure.
		if !isPedidoNaoEncontrado(err) {
			return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Connected, but the API key was rejected", nil, "")
		}
	}
	return r.SendEnvelope(map[string]string{"message": "Connection successful"})
}

func isPedidoNaoEncontrado(err error) bool {
	return errors.Is(err, xprocess.ErrPedidoNaoEncontrado)
}
