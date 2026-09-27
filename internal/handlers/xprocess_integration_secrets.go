package handlers

import (
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// xprocessAPIKeyPlaceholder is what a chatbot flow author types into a
// header value to use the organization's X2 credential — never the key
// itself. Resolved server-side only, right before the HTTP call is made;
// it is never written to SessionData and never used to populate a template
// *variable*, so it cannot leak into a later message or log line that
// happens to dump session variables (design §3: "Chave do X2 dentro de
// fluxos do bot"). The resolved header value does still pass, as literal
// input text, through executeConfiguredAPI's existing processTemplate call
// on header values — a no-op unless that text itself contains "{{...}}".
const xprocessAPIKeyPlaceholder = "{{integrations.xprocess.api_key}}"

// resolveIntegrationSecrets returns a copy of config with the X2 API key
// placeholder substituted into any "headers" value that contains it. Fails
// closed: if the placeholder is present but no active integration is
// configured for the organization, returns an error instead of sending the
// literal placeholder string to whatever URL the node targets.
func (a *App) resolveIntegrationSecrets(orgID uuid.UUID, config map[string]any) (map[string]any, error) {
	headers, ok := config["headers"].(map[string]any)
	if !ok {
		return config, nil
	}

	needsKey := false
	for _, v := range headers {
		if s, ok := v.(string); ok && strings.Contains(s, xprocessAPIKeyPlaceholder) {
			needsKey = true
			break
		}
	}
	if !needsKey {
		return config, nil
	}

	var integ models.XProcessIntegration
	if err := a.DB.Where("organization_id = ? AND is_active = ?", orgID, true).First(&integ).Error; err != nil {
		return nil, errors.New("xprocess integration is not configured for this organization")
	}

	// Fail closed: only substitute the key into a request that actually
	// targets the integration's own base_url. Without this, anyone who can
	// edit a flow (a much lower bar than xprocess_integration:write, which
	// gates the credential itself) could point an api_call node at a server
	// they control, put the placeholder in a header, and have the decrypted
	// key handed to them. Checked against the RAW, pre-template url — never
	// a rendered one — and the boundary requires the trailing "/"
	// specifically: a naive strings.HasPrefix against "https://host.com"
	// (no trailing slash) would also accept the userinfo trick
	// "https://host.com@evil.com/...", which requiring ".../" as the
	// boundary closes off.
	rawURL, _ := config["url"].(string)
	allowedPrefix := strings.TrimRight(integ.BaseURL, "/") + "/"
	if !strings.HasPrefix(rawURL, allowedPrefix) {
		return nil, fmt.Errorf("xprocess: api_call url %q does not target the configured integration base_url", rawURL)
	}

	integ.DecryptSecrets(a.Config.App.EncryptionKey)

	resolvedHeaders := make(map[string]any, len(headers))
	for k, v := range headers {
		if s, ok := v.(string); ok {
			resolvedHeaders[k] = strings.ReplaceAll(s, xprocessAPIKeyPlaceholder, integ.APIKey)
		} else {
			resolvedHeaders[k] = v
		}
	}

	resolvedConfig := make(map[string]any, len(config))
	maps.Copy(resolvedConfig, config)
	resolvedConfig["headers"] = resolvedHeaders
	return resolvedConfig, nil
}

// ResolveIntegrationSecretsForTest exposes resolveIntegrationSecrets to
// tests in this package, same reasoning as ReconcileXProcessLinkForTest.
func (a *App) ResolveIntegrationSecretsForTest(orgID uuid.UUID, config map[string]any) (map[string]any, error) {
	return a.resolveIntegrationSecrets(orgID, config)
}
