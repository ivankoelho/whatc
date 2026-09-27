# Central de Vendas — Entrega 2 (XProcess) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Conciliar automaticamente as oportunidades de venda com o ERP X2 (via número do pedido + documento registrados pelo agente) e permitir que o bot responda status de pedido ao cliente, reaproveitando a mesma credencial.

**Architecture:** Um pacote novo `pkg/xprocess` isola o cliente HTTP e o parsing das respostas do X2 (mesmo papel que `pkg/whatsapp` já tem pro Meta Graph API). A credencial fica numa tabela por organização (`xprocess_integrations`), criptografada como `WhatsAppAccount.AccessToken` já é. O vínculo pedido↔oportunidade vive em `sales_opportunity_xprocess_links`, histórico (1-para-muitos, no máximo um em aberto por vez). Um processo em segundo plano (`XProcessReconciler`, mesmo padrão de `SLAProcessor`: ticker + goroutine) roda uma vez por dia depois das ~2h e aplica a máquina de estados do design. O bot reaproveita o nó `api_call` já existente no construtor de fluxos, resolvendo a chave da integração só no servidor via um placeholder de header.

**Tech Stack:** Go (backend, GORM/Postgres), Vue 3 + TypeScript (frontend), fastglue (router), testify (testes Go), vitest (testes frontend).

## Global Constraints

- Design de referência: `docs/superpowers/specs/2026-09-27-central-vendas-entrega2-xprocess-design.md` — todo requisito deste plano vem de lá; qualquer dúvida de comportamento, essa é a fonte.
- `documento` é sempre CPF **ou** CNPJ (nunca assumir só CPF).
- Nenhuma chamada ao X2 acontece no momento em que o agente registra o pedido (design §6) — só o job diário chama.
- `resolved_at` só é preenchido quando o vínculo chega a um estado terminal de verdade (design §5) — nunca calculado antecipadamente na mesma rodada que viu o gatilho (bug já corrigido no design, não repetir na implementação).
- `first_closed_at` é gravado uma única vez, nunca reescrito.
- `consecutive_not_found` conta rodadas do job, não dias corridos.
- Vínculo resolvido (`resolved_at` não nulo) é imutável — nunca sobrescrito por um novo registro do agente; um novo registro cria uma linha nova.
- Textos voltados ao agente sobre 404 usam "não localizado", nunca "inválido".
- A chave do X2 nunca aparece em claro depois de salva, nem em JSON de fluxo de chatbot, nem em log.
- Go 1.26 (já em produção desde o sync do upstream) — sem novidade de toolchain aqui.
- **GORM map-based `Updates()`/`Update()` usa a chave como nome literal de coluna**, não resolve por reflection como um struct. Todo campo com "XProcess" ou outro prefixo de letra maiúscula isolada (`XProcessNumPedido`, `XProcessDocumento`, `StatusXProcess`) precisa de `gorm:"column:..."` explícito — o default do GORM insere um `_` extra (`x_process_num_pedido`, não `xprocess_num_pedido`), verificado empiricamente contra `gorm.io/gorm/schema.NamingStrategy` ao escrever este plano. Já aconteceu de verdade nesta base com `Contact.CPFCNPJ` (commit `00e573c`) — toda referência a essa coluna neste plano usa `cpfcnpj`, nunca `cpf_cnpj`.

---

## Mapa de arquivos

| Arquivo | Responsabilidade |
|---|---|
| `pkg/xprocess/client.go` (novo) | Cliente HTTP do X2: `ConsultarPedido`, parsing de decimal BR, `SummarizePedido` |
| `pkg/xprocess/client_test.go` (novo) | Testes do cliente contra um `httptest.Server` fake |
| `internal/models/xprocess_integration.go` (novo) | Model `XProcessIntegration` + criptografia |
| `internal/models/sales_opportunity_xprocess_link.go` (novo) | Model `SalesOpportunityXProcessLink` |
| `internal/models/sales_opportunity.go` (modificar) | + `XProcessNumPedido`/`XProcessDocumento` |
| `internal/contactutil/contactutil.go` (modificar) | + `NormalizeDocumento` |
| `internal/database/postgres.go` (modificar) | Registra os 2 models novos no `AutoMigrate` |
| `internal/models/roles.go` (modificar) | + `ResourceXProcessIntegration` e permissões default |
| `internal/database/permissions_backfill.go` (modificar) | + `BackfillXProcessIntegrationPermission` |
| `internal/handlers/xprocess_integration.go` (novo) | Handlers de credencial (get/upsert/test) |
| `internal/handlers/sales_opportunity_xprocess_link.go` (novo) | Handlers de vínculo (get/upsert) na oportunidade |
| `internal/handlers/xprocess_reconciler.go` (novo) | Job diário: `reconcileXProcessLink`, `RunXProcessReconciliation`, `XProcessReconciler` |
| `internal/handlers/xprocess_integration_secrets.go` (novo) | `resolveIntegrationSecrets` pro nó `api_call` do bot |
| `internal/handlers/chatbot_graph_runner.go` (modificar) | `execChatAPICall` chama `resolveIntegrationSecrets` |
| `cmd/whatomate/main.go` (modificar) | Registra rotas novas, roda o backfill, inicia o `XProcessReconciler` |
| `frontend/src/views/settings/XProcessIntegrationView.vue` (novo) | Aba "ERP X2" em Integrações |
| `frontend/src/views/settings/IntegrationsSettingsHubView.vue` (modificar) | + aba nova |
| `frontend/src/services/api.ts` (modificar) | Funções de API pra integração + vínculo |
| `frontend/src/components/sales/XProcessLinkForm.vue` (novo) | Formulário inline número+documento na oportunidade |
| `frontend/src/i18n/locales/en.json` / `pt-BR.json` (modificar) | Chaves novas |

---

## Task 1: Cliente do X2 (`pkg/xprocess`)

**Files:**
- Create: `pkg/xprocess/client.go`
- Create: `pkg/xprocess/client_test.go`

**Interfaces:**
- Produces: `xprocess.Client`, `xprocess.New(log logf.Logger, baseURL string) *Client`, `xprocess.PedidoItem`, `xprocess.PedidoResumo`, `xprocess.ErrPedidoNaoEncontrado`, `(*Client) ConsultarPedido(ctx context.Context, apiKey, numPedido, documento string) ([]PedidoItem, error)`, `xprocess.SummarizePedido(items []PedidoItem) (PedidoResumo, error)`, `xprocess.ParseDecimalBR(s string) (float64, error)`.

- [ ] **Step 1: Write the failing tests**

```go
// pkg/xprocess/client_test.go
package xprocess

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/logf"
)

func testLog() logf.Logger { return logf.New(logf.Opts{}) }

func TestParseDecimalBR(t *testing.T) {
	got, err := ParseDecimalBR("230,0418")
	require.NoError(t, err)
	assert.InDelta(t, 230.0418, got, 0.0001)

	got, err = ParseDecimalBR("0,0")
	require.NoError(t, err)
	assert.Equal(t, 0.0, got)

	_, err = ParseDecimalBR("not-a-number")
	assert.Error(t, err)
}

func TestClient_ConsultarPedido_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/pedido", r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"pedido":[
			{"cod_empresa":"7","num_pedido":"666","cod_cliente":"9446","cod_vendedor":"2586","cod_produto":"22393","quantidade":"13,62","total":"230,0418","status":"SEPARACAO","data_venda":null},
			{"cod_empresa":"7","num_pedido":"666","cod_cliente":"9446","cod_vendedor":"2586","cod_produto":"17736","quantidade":"18,0","total":"178,2","status":"SEPARACAO","data_venda":null}
		]}`))
	}))
	defer srv.Close()

	c := New(testLog(), srv.URL)
	items, err := c.ConsultarPedido(context.Background(), "test-key", "666", "12345678900")
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "SEPARACAO", items[0].Status)
	assert.Equal(t, "7", items[0].CodEmpresa)
}

func TestClient_ConsultarPedido_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Pedido nao encontrado"}`))
	}))
	defer srv.Close()

	c := New(testLog(), srv.URL)
	_, err := c.ConsultarPedido(context.Background(), "test-key", "666", "00000000000")
	assert.True(t, errors.Is(err, ErrPedidoNaoEncontrado))
}

func TestClient_ConsultarPedido_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(testLog(), srv.URL)
	_, err := c.ConsultarPedido(context.Background(), "test-key", "666", "00000000000")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrPedidoNaoEncontrado))
}

func TestSummarizePedido(t *testing.T) {
	items := []PedidoItem{
		{CodEmpresa: "7", NumPedido: "666", CodVendedor: "2586", Status: "SEPARACAO", Total: "230,0418"},
		{CodEmpresa: "7", NumPedido: "666", CodVendedor: "2586", Status: "SEPARACAO", Total: "178,2"},
	}
	resumo, err := SummarizePedido(items)
	require.NoError(t, err)
	assert.Equal(t, "SEPARACAO", resumo.Status)
	assert.Equal(t, "7", resumo.CodEmpresa)
	assert.Equal(t, "2586", resumo.CodVendedor)
	assert.InDelta(t, 408.2418, resumo.ValorTotal, 0.0001)
	assert.Len(t, resumo.Itens, 2)
}

func TestSummarizePedido_Empty(t *testing.T) {
	_, err := SummarizePedido(nil)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/xprocess/... -v`
Expected: FAIL — package `xprocess` does not exist yet (build error).

- [ ] **Step 3: Write the implementation**

```go
// pkg/xprocess/client.go
// Package xprocess is a thin client for the customer's own X2 ERP API
// (https://api.atacadaodospisos.com.br/docs). The API is NOT real-time: it
// is a daily CSV re-export of X2, refreshed around 2h. See
// docs/superpowers/specs/2026-09-27-central-vendas-entrega2-xprocess-design.md
// §1 for the full context — every design decision here follows from that.
package xprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zerodha/logf"
)

// DefaultTimeout bounds a single request to the X2 API.
const DefaultTimeout = 15 * time.Second

// ErrPedidoNaoEncontrado is returned when the X2 API responds 404 for
// POST /api/pedido — either the numero_pedido/documento combination is
// wrong, or (just as likely, given the API is D-1) the pedido legitimately
// has not reached the API's daily data load yet. Callers must not treat
// this as "invalid" on its own — see the design's §5/§8 wording rule.
var ErrPedidoNaoEncontrado = errors.New("xprocess: pedido nao encontrado")

// Client talks to one organization's X2 API instance.
type Client struct {
	HTTPClient *http.Client
	Log        logf.Logger
	baseURL    string
}

// New creates a client for the given base URL (e.g.
// "https://api.atacadaodospisos.com.br", no trailing slash expected but
// tolerated). There is no package-level default base URL — unlike Meta's
// Graph API, every organization configures its own X2 instance.
func New(log logf.Logger, baseURL string) *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: DefaultTimeout},
		Log:        log,
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// PedidoItem is one line item of a pedido, as returned by
// POST /api/pedido and GET /api/vendas. Only the fields this integration
// actually reads are declared — unknown JSON fields are ignored by
// encoding/json, no need to mirror the full X2 schema.
type PedidoItem struct {
	CodEmpresa  string  `json:"cod_empresa"`
	NumPedido   string  `json:"num_pedido"`
	CodCliente  string  `json:"cod_cliente"`
	CodVendedor string  `json:"cod_vendedor"`
	Status      string  `json:"status"`
	Total       string  `json:"total"` // BR decimal, comma separator — see ParseDecimalBR
	DataVenda   *string `json:"data_venda"`
}

type pedidoResponse struct {
	OK     bool         `json:"ok"`
	Pedido []PedidoItem `json:"pedido"`
}

// ConsultarPedido calls POST /api/pedido with the exact pedido number and
// the customer's documento (CPF or CNPJ). The X2 API validates both
// together — numero_pedido alone is ambiguous (only unique per loja, not
// globally), so documento is never optional here (design §1, §3).
func (c *Client) ConsultarPedido(ctx context.Context, apiKey, numPedido, documento string) ([]PedidoItem, error) {
	payload, err := json.Marshal(map[string]string{
		"numero_pedido": numPedido,
		"documento":     documento,
	})
	if err != nil {
		return nil, fmt.Errorf("xprocess: failed to encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/pedido", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("xprocess: failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xprocess: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("xprocess: failed to read response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrPedidoNaoEncontrado
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("xprocess: unexpected status %d: %s", resp.StatusCode, truncate(string(body), 300))
	}

	var parsed pedidoResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("xprocess: failed to parse response: %w", err)
	}
	return parsed.Pedido, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ParseDecimalBR parses an X2 numeric value, which always comes as a string
// with a comma decimal separator (e.g. "230,0418"), never a JSON number.
func ParseDecimalBR(s string) (float64, error) {
	return strconv.ParseFloat(strings.Replace(s, ",", ".", 1), 64)
}

// PedidoResumo aggregates a pedido's line items into the summary the
// reconciliation job actually needs: one status, one seller, one total.
type PedidoResumo struct {
	CodEmpresa  string
	CodVendedor string
	Status      string
	DataVenda   *string
	ValorTotal  float64
	Itens       []PedidoItem
}

// SummarizePedido aggregates ConsultarPedido's line items. Every item of one
// pedido shares the same status/empresa/vendedor (confirmed against the real
// API — see the design's §1 findings), so those come from the first item;
// ValorTotal sums each item's Total.
func SummarizePedido(items []PedidoItem) (PedidoResumo, error) {
	if len(items) == 0 {
		return PedidoResumo{}, errors.New("xprocess: cannot summarize an empty pedido")
	}
	resumo := PedidoResumo{
		CodEmpresa:  items[0].CodEmpresa,
		CodVendedor: items[0].CodVendedor,
		Status:      items[0].Status,
		DataVenda:   items[0].DataVenda,
		Itens:       items,
	}
	for _, item := range items {
		v, err := ParseDecimalBR(item.Total)
		if err != nil {
			return PedidoResumo{}, fmt.Errorf("xprocess: failed to parse item total %q: %w", item.Total, err)
		}
		resumo.ValorTotal += v
	}
	return resumo, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/xprocess/... -v`
Expected: PASS — all 6 tests green.

- [ ] **Step 5: Commit**

```bash
git add pkg/xprocess
git commit -m "feat(xprocess): add X2 API client (ConsultarPedido, SummarizePedido)"
```

---

## Task 2: Model + criptografia da credencial (`XProcessIntegration`)

**Files:**
- Create: `internal/models/xprocess_integration.go`
- Modify: `internal/database/postgres.go` (adiciona ao `GetMigrationModels()`)

**Interfaces:**
- Consumes: `crypto.EncryptFields(key string, fields ...*string) error`, `crypto.DecryptFields(key string, fields ...*string)` (já existem, usados por `WhatsAppAccount`).
- Produces: `models.XProcessIntegration{BaseModel, OrganizationID uuid.UUID, BaseURL string, APIKey string, IsActive bool}`, `(*XProcessIntegration) EncryptSecrets(key string) error`, `(*XProcessIntegration) DecryptSecrets(key string)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/models/xprocess_integration_test.go
package models_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestXProcessIntegration_EncryptDecryptSecrets(t *testing.T) {
	key := "test-encryption-key-for-models-longer-than-32-chars"
	integ := &models.XProcessIntegration{
		BaseURL: "https://api.atacadaodospisos.com.br",
		APIKey:  "plain-api-key",
	}
	require.NoError(t, integ.EncryptSecrets(key))
	assert.NotEqual(t, "plain-api-key", integ.APIKey, "api key must not stay in plaintext after EncryptSecrets")

	integ.DecryptSecrets(key)
	assert.Equal(t, "plain-api-key", integ.APIKey)
}

func TestXProcessIntegration_TableName(t *testing.T) {
	assert.Equal(t, "xprocess_integrations", models.XProcessIntegration{}.TableName())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/models/... -run TestXProcessIntegration -v`
Expected: FAIL — `models.XProcessIntegration` undefined.

- [ ] **Step 3: Write the model**

```go
// internal/models/xprocess_integration.go
package models

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
)

// XProcessIntegration holds one organization's credential for the customer's
// X2 ERP API (design §4). One row per organization — OrganizationID is
// unique, mirroring how WhatsAppAccount scopes its own encrypted secrets.
type XProcessIntegration struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;uniqueIndex;not null" json:"organization_id"`
	BaseURL        string    `gorm:"size:255;not null" json:"base_url"`
	// APIKey is encrypted at rest via EncryptSecrets/DecryptSecrets, same
	// pattern as WhatsAppAccount.AccessToken. Never serialize this field
	// back to the frontend in plaintext — handlers must omit it, not just
	// rely on json:"-" (a caller could still read it straight off the
	// struct), so the "-" tag here is a second layer, not the only one.
	APIKey   string `gorm:"type:text" json:"-"`
	IsActive bool   `gorm:"default:true" json:"is_active"`
}

func (XProcessIntegration) TableName() string { return "xprocess_integrations" }

// EncryptSecrets encrypts APIKey in place before a Create/Save.
func (x *XProcessIntegration) EncryptSecrets(encryptionKey string) error {
	return crypto.EncryptFields(encryptionKey, &x.APIKey)
}

// DecryptSecrets decrypts APIKey in place after a read from the DB.
func (x *XProcessIntegration) DecryptSecrets(encryptionKey string) {
	crypto.DecryptFields(encryptionKey, &x.APIKey)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/models/... -run TestXProcessIntegration -v`
Expected: PASS

- [ ] **Step 5: Register the model for AutoMigrate**

In `internal/database/postgres.go`, inside `GetMigrationModels()`, add a line next to `{"WhatsAppAccount", &models.WhatsAppAccount{}}`:

```go
		{"WhatsAppAccount", &models.WhatsAppAccount{}},
		{"XProcessIntegration", &models.XProcessIntegration{}},
```

- [ ] **Step 6: Build to confirm the migration list compiles**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add internal/models/xprocess_integration.go internal/models/xprocess_integration_test.go internal/database/postgres.go
git commit -m "feat(models): add XProcessIntegration (encrypted, one per organization)"
```

---

## Task 3: Permissão nova + backfill

**Files:**
- Modify: `internal/models/roles.go`
- Modify: `internal/database/permissions_backfill.go`
- Create: `internal/database/permissions_backfill_xprocess_test.go`
- Modify: `cmd/whatomate/main.go` (roda o backfill na inicialização)

**Interfaces:**
- Produces: `models.ResourceXProcessIntegration = "xprocess_integration"`; `database.BackfillXProcessIntegrationPermission(db *gorm.DB, lo logf.Logger) error`.

- [ ] **Step 1: Add the resource constant and default permissions**

In `internal/models/roles.go`, next to `ResourceSalesOpportunities`:

```go
	ResourceSalesOpportunities      = "sales_opportunities"
	ResourceXProcessIntegration     = "xprocess_integration"
```

Inside `DefaultPermissions()`, add (near the other integration-style resources, e.g. next to the `ResourceAPIKeys` block):

```go
		{Resource: ResourceXProcessIntegration, Action: ActionRead, Description: "View the X2 ERP integration settings"},
		{Resource: ResourceXProcessIntegration, Action: ActionWrite, Description: "Configure the X2 ERP integration credential"},
```

- [ ] **Step 2: Write the failing backfill test**

Follows `permissions_backfill_sales_test.go`'s exact pattern — same `testutil.SetupTestDB`, `cleanAll`, `database.SeedPermissionsAndRoles`, `testutil.CreateTestOrganization`, `testutil.CreateTestRoleWithKeys`, `roleKeys` and `testLog` helpers already defined in this package's other test files (`database_test.go`, `permissions_backfill_test.go`) — do not redefine any of them.

```go
// internal/database/permissions_backfill_xprocess_test.go
package database_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackfillXProcessIntegrationPermission_GrantsToRolesWithAccountsWrite(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	adminRole := testutil.CreateTestRoleWithKeys(t, db, org.ID, "admin-like", []string{"accounts:write"})
	agentRole := testutil.CreateTestRoleWithKeys(t, db, org.ID, "agent-like", []string{"chat:write"})

	require.NoError(t, database.BackfillXProcessIntegrationPermission(db, testLog()))

	adminKeys := roleKeys(t, db, adminRole.ID)
	assert.Contains(t, adminKeys, "xprocess_integration:read")
	assert.Contains(t, adminKeys, "xprocess_integration:write")

	agentKeys := roleKeys(t, db, agentRole.ID)
	assert.NotContains(t, agentKeys, "xprocess_integration:read",
		"a role without accounts:write must not gain the X2 integration permission")
}

func TestBackfillXProcessIntegrationPermission_IdempotentPerOrganization(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID, "admin-like", []string{"accounts:write"})

	require.NoError(t, database.BackfillXProcessIntegrationPermission(db, testLog()))
	require.Contains(t, roleKeys(t, db, role.ID), "xprocess_integration:write")

	// Simulate the admin manually revoking it after the first backfill.
	require.NoError(t, db.Exec(`
		DELETE FROM role_permissions
		WHERE custom_role_id = ?
		  AND permission_id = (SELECT id FROM permissions WHERE resource = ? AND action = ?)`,
		role.ID, "xprocess_integration", "write").Error)

	require.NoError(t, database.BackfillXProcessIntegrationPermission(db, testLog()))
	assert.NotContains(t, roleKeys(t, db, role.ID), "xprocess_integration:write",
		"second run must not re-grant to an org already migrated")
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/database/... -run TestBackfillXProcessIntegrationPermission -v`
Expected: FAIL — `database.BackfillXProcessIntegrationPermission` undefined.

- [ ] **Step 4: Write the backfill function**

Append to `internal/database/permissions_backfill.go`, following `BackfillContactNamePermission`'s exact shape (single new permission, granted based on an existing related one — here, `accounts:write`, since a WhatsApp account's access token is exactly as sensitive as the X2 API key, and today's admins who manage one already manage the other):

```go
// BackfillXProcessIntegrationPermission concede xprocess_integration:{read,write}
// aos papéis que já administram credenciais externas (accounts:write — quem
// configura o WhatsApp Business Account já é quem deveria configurar o X2).
// Necessário porque FixSystemRolePermissions pula qualquer papel que já tem
// alguma permissão, então esta permissão nova nunca alcançaria organizações
// já existentes sem este backfill (mesma razão de existir de
// BackfillContactNamePermission).
//
// Puramente aditivo: nunca revoga nada.
func BackfillXProcessIntegrationPermission(db *gorm.DB, lo logf.Logger) error {
	var seeded int64
	if err := db.Model(&models.Permission{}).
		Where("resource = ? AND action = ?", models.ResourceXProcessIntegration, models.ActionWrite).
		Count(&seeded).Error; err != nil {
		return fmt.Errorf("failed to count the xprocess_integration permission: %w", err)
	}
	if seeded == 0 {
		lo.Warn("xprocess_integration permission not seeded yet, did nothing")
		return nil
	}

	res := db.Exec(`
		INSERT INTO role_permissions (custom_role_id, permission_id)
		SELECT DISTINCT r.id, target.id
		FROM custom_roles r
		JOIN role_permissions rp ON rp.custom_role_id = r.id
		JOIN permissions src ON src.id = rp.permission_id
		CROSS JOIN permissions target
		WHERE r.deleted_at IS NULL
		  AND target.resource = ?
		  AND src.resource = ? AND src.action = ?
		  AND NOT EXISTS (
		    SELECT 1
		    FROM custom_roles r2
		    JOIN role_permissions rp2 ON rp2.custom_role_id = r2.id
		    JOIN permissions p2 ON p2.id = rp2.permission_id
		    WHERE r2.organization_id = r.organization_id
		      AND r2.deleted_at IS NULL
		      AND p2.resource = ?
		  )
		ON CONFLICT DO NOTHING`,
		models.ResourceXProcessIntegration,
		models.ResourceAccounts, models.ActionWrite,
		models.ResourceXProcessIntegration,
	)
	if res.Error != nil {
		return fmt.Errorf("failed to grant the xprocess_integration permission: %w", res.Error)
	}

	if res.RowsAffected == 0 {
		lo.Info("xprocess_integration backfill: nothing pending")
		return nil
	}
	lo.Info("xprocess_integration backfill complete", "links_granted", res.RowsAffected)
	return nil
}
```

This grants both `read` and `write` in the same statement because `target.resource = ?` (no `target.action` filter) matches every action of that resource — matching `BackfillContactNamePermission`'s pattern of a single target resource.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/database/... -run TestBackfillXProcessIntegrationPermission -v`
Expected: PASS

- [ ] **Step 6: Wire the backfill into startup**

In `cmd/whatomate/main.go`, next to the existing `database.BackfillSalesOpportunityPermissions(db, lo)` call:

```go
		if err := database.BackfillXProcessIntegrationPermission(db, lo); err != nil {
			lo.Error("Failed to backfill xprocess_integration permission", "error", err)
		}
```

- [ ] **Step 7: Build and run the full backfill test file**

Run: `go build ./... && go test ./internal/database/... -v`
Expected: PASS, no regressions in sibling backfill tests.

- [ ] **Step 8: Commit**

```bash
git add internal/models/roles.go internal/database/permissions_backfill.go internal/database/permissions_backfill_xprocess_test.go cmd/whatomate/main.go
git commit -m "feat(permissions): add xprocess_integration resource + backfill"
```

---

## Task 4: Handlers da credencial (get / upsert / testar conexão)

**Files:**
- Create: `internal/handlers/xprocess_integration.go`
- Create: `internal/handlers/xprocess_integration_test.go`
- Modify: `cmd/whatomate/main.go` (registra as 3 rotas)

**Interfaces:**
- Consumes: `xprocess.New`, `(*xprocess.Client) ConsultarPedido` indirectly not needed here (test-connection uses a lighter call — see Step 4), `a.requireAuth`, `a.decodeRequest`, `models.ResourceXProcessIntegration`.
- Produces: `(a *App) GetXProcessIntegration(r *fastglue.Request) error`, `(a *App) UpsertXProcessIntegration(r *fastglue.Request) error`, `(a *App) TestXProcessIntegrationConnection(r *fastglue.Request) error`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/handlers/xprocess_integration_test.go
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestApp_UpsertXProcessIntegration_CreatesEncrypted(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "admin", []string{"xprocess_integration:read", "xprocess_integration:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	req := testutil.NewJSONRequest(t, map[string]any{
		"base_url": "https://api.atacadaodospisos.com.br",
		"api_key":  "plain-key-123",
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UpsertXProcessIntegration(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var stored models.XProcessIntegration
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).First(&stored).Error)
	assert.NotEqual(t, "plain-key-123", stored.APIKey, "api key must be encrypted at rest")
	stored.DecryptSecrets(app.Config.App.EncryptionKey)
	assert.Equal(t, "plain-key-123", stored.APIKey)

	// Response body must never carry the key back, encrypted or not.
	body := string(testutil.GetResponseBody(req))
	assert.NotContains(t, body, "plain-key-123")
	assert.NotContains(t, body, "api_key")
}

func TestApp_UpsertXProcessIntegration_UpdatesExisting(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "admin", []string{"xprocess_integration:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	first := testutil.NewJSONRequest(t, map[string]any{"base_url": "https://old.example.com", "api_key": "key-1"})
	testutil.SetAuthContext(first, org.ID, user.ID)
	require.NoError(t, app.UpsertXProcessIntegration(first))

	second := testutil.NewJSONRequest(t, map[string]any{"base_url": "https://new.example.com", "api_key": "key-2"})
	testutil.SetAuthContext(second, org.ID, user.ID)
	require.NoError(t, app.UpsertXProcessIntegration(second))

	var all []models.XProcessIntegration
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).Find(&all).Error)
	require.Len(t, all, 1, "upsert must update the single row, not create a second one")
	assert.Equal(t, "https://new.example.com", all[0].BaseURL)
}

func TestApp_GetXProcessIntegration_NeverReturnsKey(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "admin", []string{"xprocess_integration:read", "xprocess_integration:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	upsert := testutil.NewJSONRequest(t, map[string]any{"base_url": "https://api.atacadaodospisos.com.br", "api_key": "secret-key"})
	testutil.SetAuthContext(upsert, org.ID, user.ID)
	require.NoError(t, app.UpsertXProcessIntegration(upsert))

	get := testutil.NewGETRequest(t)
	testutil.SetAuthContext(get, org.ID, user.ID)
	require.NoError(t, app.GetXProcessIntegration(get))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(get))
	body := string(testutil.GetResponseBody(get))
	assert.NotContains(t, body, "secret-key")

	var resp struct {
		Status string `json:"status"`
		Data   struct {
			BaseURL     string `json:"base_url"`
			IsConfigured bool  `json:"is_configured"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(get), &resp))
	assert.Equal(t, "https://api.atacadaodospisos.com.br", resp.Data.BaseURL)
	assert.True(t, resp.Data.IsConfigured)
}

func TestApp_TestXProcessIntegrationConnection(t *testing.T) {
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/liveness" {
			w.WriteHeader(http.StatusOK)
			return
		}
		assert.Equal(t, "/api/vendedores", r.URL.Path)
		assert.Equal(t, "the-key", r.Header.Get("x-api-key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"total":0,"vendedores":[]}`))
	}))
	defer meta.Close()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "admin", []string{"xprocess_integration:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"base_url": meta.URL, "api_key": "the-key"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.TestXProcessIntegrationConnection(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
}

func TestApp_TestXProcessIntegrationConnection_Unreachable(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "admin", []string{"xprocess_integration:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"base_url": "http://127.0.0.1:1", "api_key": "the-key"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.TestXProcessIntegrationConnection(req))
	testutil.AssertErrorResponse(t, req, fasthttp.StatusBadGateway, "")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/handlers/... -run TestApp_.*XProcessIntegration -v`
Expected: FAIL — handlers undefined.

- [ ] **Step 3: Write the handlers**

```go
// internal/handlers/xprocess_integration.go
package handlers

import (
	"context"
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
// decision). Checks /liveness (no auth) then /api/vendedores (with the
// key) so both "wrong URL" and "wrong key" are distinguished by the error.
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

	livenessReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, req.BaseURL+"/liveness", nil)
	livenessResp, err := a.HTTPClient.Do(livenessReq)
	if err != nil || livenessResp.StatusCode >= 300 {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Could not reach base_url", nil, "")
	}
	_ = livenessResp.Body.Close()

	client := xprocess.New(a.Log, req.BaseURL)
	// /api/vendedores ignores cod_vendedor/limit when omitted and returns
	// the full list — a cheap, side-effect-free way to prove the key
	// authenticates without needing a real pedido/documento pair.
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
```

Add the tiny `isPedidoNaoEncontrado` helper (keeps the handler from importing `errors` just for one check, and keeps the intent obvious at the call site):

```go
func isPedidoNaoEncontrado(err error) bool {
	return errors.Is(err, xprocess.ErrPedidoNaoEncontrado)
}
```

(add `"errors"` to the import block)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/handlers/... -run TestApp_.*XProcessIntegration -v`
Expected: PASS

- [ ] **Step 5: Register routes**

In `cmd/whatomate/main.go`, next to the sales-opportunities routes:

```go
	g.GET("/api/xprocess-integration", app.GetXProcessIntegration)
	g.PUT("/api/xprocess-integration", app.UpsertXProcessIntegration)
	g.POST("/api/xprocess-integration/test", app.TestXProcessIntegrationConnection)
```

- [ ] **Step 6: Build and run full handlers package**

Run: `go build ./... && go vet ./internal/handlers/...`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add internal/handlers/xprocess_integration.go internal/handlers/xprocess_integration_test.go cmd/whatomate/main.go
git commit -m "feat(handlers): X2 integration credential endpoints (get/upsert/test)"
```

---

## Task 5: Aba "ERP X2" em Configurações → Integrações

**Files:**
- Create: `frontend/src/views/settings/XProcessIntegrationView.vue`
- Modify: `frontend/src/views/settings/IntegrationsSettingsHubView.vue`
- Modify: `frontend/src/services/api.ts`
- Modify: `frontend/src/i18n/locales/en.json`, `frontend/src/i18n/locales/pt-BR.json`

**Interfaces:**
- Consumes: `GET/PUT /api/xprocess-integration`, `POST /api/xprocess-integration/test` (Task 4).
- Produces: `xprocessIntegrationService` in `api.ts` with `get()`, `upsert(payload)`, `test(payload)`.

- [ ] **Step 1: Add the API service functions**

In `frontend/src/services/api.ts`, add near the other settings-style services:

```typescript
export interface XProcessIntegrationStatus {
  base_url: string
  is_active: boolean
  is_configured: boolean
}

export const xprocessIntegrationService = {
  get: () => api.get<{ data: XProcessIntegrationStatus }>('/xprocess-integration'),
  upsert: (payload: { base_url: string; api_key: string }) =>
    api.put<{ data: XProcessIntegrationStatus }>('/xprocess-integration', payload),
  test: (payload: { base_url: string; api_key: string }) =>
    api.post<{ data: { message: string } }>('/xprocess-integration/test', payload),
}
```

(Match the existing `api` axios instance and response-unwrapping convention already used by neighboring services in this same file — copy the exact style of e.g. `apiKeysService` rather than inventing a new one.)

- [ ] **Step 2: Add i18n keys**

In `frontend/src/i18n/locales/en.json`, under `nav`:
```json
    "xprocessIntegration": "ERP X2",
```
And a new top-level section (place it near other settings-integration sections):
```json
  "xprocessIntegration": {
    "title": "ERP X2 Integration",
    "description": "Credential used by the daily reconciliation job and by chatbot flows that look up order status.",
    "baseUrl": "Base URL",
    "apiKey": "API Key",
    "apiKeyConfigured": "Configured — enter a new key to replace it",
    "testConnection": "Test connection",
    "testSuccess": "Connection successful",
    "testFailure": "Could not connect — check the URL and key",
    "save": "Save"
  },
```
In `frontend/src/i18n/locales/pt-BR.json`, same keys under `nav`:
```json
    "xprocessIntegration": "ERP X2",
```
and:
```json
  "xprocessIntegration": {
    "title": "Integração ERP X2",
    "description": "Credencial usada pelo job diário de conciliação e por fluxos do bot que consultam status de pedido.",
    "baseUrl": "URL base",
    "apiKey": "Chave de API",
    "apiKeyConfigured": "Configurada — digite uma nova chave para substituir",
    "testConnection": "Testar conexão",
    "testSuccess": "Conexão bem-sucedida",
    "testFailure": "Não foi possível conectar — confira a URL e a chave",
    "save": "Salvar"
  },
```

- [ ] **Step 3: Write the view**

```vue
<!-- frontend/src/views/settings/XProcessIntegrationView.vue -->
<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { xprocessIntegrationService } from '@/services/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

const { t } = useI18n()

const baseUrl = ref('https://api.atacadaodospisos.com.br')
const apiKey = ref('')
const isConfigured = ref(false)
const testing = ref(false)
const saving = ref(false)

async function load() {
  const res = await xprocessIntegrationService.get()
  if (res.data.data.base_url) baseUrl.value = res.data.data.base_url
  isConfigured.value = res.data.data.is_configured
}

async function testConnection() {
  if (!apiKey.value) {
    toast.error(t('xprocessIntegration.testFailure'))
    return
  }
  testing.value = true
  try {
    await xprocessIntegrationService.test({ base_url: baseUrl.value, api_key: apiKey.value })
    toast.success(t('xprocessIntegration.testSuccess'))
  } catch {
    toast.error(t('xprocessIntegration.testFailure'))
  } finally {
    testing.value = false
  }
}

async function save() {
  if (!apiKey.value) return
  saving.value = true
  try {
    await xprocessIntegrationService.upsert({ base_url: baseUrl.value, api_key: apiKey.value })
    apiKey.value = ''
    isConfigured.value = true
    toast.success(t('common.saved'))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="max-w-lg space-y-4">
    <div>
      <h3 class="text-lg font-medium">{{ t('xprocessIntegration.title') }}</h3>
      <p class="text-sm text-muted-foreground">{{ t('xprocessIntegration.description') }}</p>
    </div>

    <div class="space-y-2">
      <Label for="xprocess-base-url">{{ t('xprocessIntegration.baseUrl') }}</Label>
      <Input id="xprocess-base-url" v-model="baseUrl" />
    </div>

    <div class="space-y-2">
      <Label for="xprocess-api-key">{{ t('xprocessIntegration.apiKey') }}</Label>
      <Input id="xprocess-api-key" v-model="apiKey" type="password"
        :placeholder="isConfigured ? t('xprocessIntegration.apiKeyConfigured') : ''" />
    </div>

    <div class="flex gap-2">
      <Button variant="outline" :disabled="testing" @click="testConnection">
        {{ t('xprocessIntegration.testConnection') }}
      </Button>
      <Button :disabled="saving || !apiKey" @click="save">
        {{ t('xprocessIntegration.save') }}
      </Button>
    </div>
  </div>
</template>
```

- [ ] **Step 4: Add the tab to the hub**

In `frontend/src/views/settings/IntegrationsSettingsHubView.vue`:

```vue
<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import APIKeysView from './APIKeysView.vue'
import WebhooksView from './WebhooksView.vue'
import CustomActionsView from './CustomActionsView.vue'
import XProcessIntegrationView from './XProcessIntegrationView.vue'

const tabs: HubTab[] = [
  { value: 'api-keys', labelKey: 'nav.apiKeys', permission: 'api_keys', component: APIKeysView },
  { value: 'webhooks', labelKey: 'nav.webhooks', permission: 'webhooks', component: WebhooksView },
  { value: 'custom-actions', labelKey: 'nav.customActions', permission: 'custom_actions', component: CustomActionsView },
  { value: 'xprocess', labelKey: 'nav.xprocessIntegration', permission: 'xprocess_integration', component: XProcessIntegrationView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="api-keys" />
</template>
```

- [ ] **Step 5: Verify it builds and lints**

Run: `cd frontend && npm run typecheck && npx eslint src/views/settings/XProcessIntegrationView.vue src/views/settings/IntegrationsSettingsHubView.vue src/services/api.ts --ext .vue,.ts`
Expected: no errors (warnings pre-existing elsewhere in the repo are fine, don't fix unrelated ones).

Run: `cd frontend && npm run build`
Expected: builds clean.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/views/settings/XProcessIntegrationView.vue frontend/src/views/settings/IntegrationsSettingsHubView.vue frontend/src/services/api.ts frontend/src/i18n/locales/en.json frontend/src/i18n/locales/pt-BR.json
git commit -m "feat(settings): ERP X2 tab in Integrações"
```

---

## Task 6: Validação estrutural de documento (CPF/CNPJ)

**Files:**
- Modify: `internal/contactutil/contactutil.go`
- Modify: `internal/contactutil/contactutil_test.go`

**Interfaces:**
- Produces: `contactutil.NormalizeDocumento(raw string) (string, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/contactutil/contactutil_test.go`:

```go
func TestNormalizeDocumento_ValidCPF(t *testing.T) {
	got, err := NormalizeDocumento("123.456.789-00")
	require.NoError(t, err)
	assert.Equal(t, "12345678900", got)
}

func TestNormalizeDocumento_ValidCNPJ(t *testing.T) {
	got, err := NormalizeDocumento("07.378.783/0001-90")
	require.NoError(t, err)
	assert.Equal(t, "07378783000190", got)
}

func TestNormalizeDocumento_InvalidLength(t *testing.T) {
	_, err := NormalizeDocumento("123456789") // 9 digits — neither CPF nor CNPJ
	assert.Error(t, err)
}

func TestNormalizeDocumento_Empty(t *testing.T) {
	_, err := NormalizeDocumento("")
	assert.Error(t, err)
}
```

(Check the top of `contactutil_test.go` for its existing import block — `require`/`assert` from testify should already be imported there; if not, add them to match the file's own convention.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/contactutil/... -run TestNormalizeDocumento -v`
Expected: FAIL — `NormalizeDocumento` undefined.

- [ ] **Step 3: Write the implementation**

Append to `internal/contactutil/contactutil.go`:

```go
// ErrInvalidDocumento is returned by NormalizeDocumento when the digits-only
// value is neither 11 (CPF) nor 14 (CNPJ) characters long.
var ErrInvalidDocumento = errors.New("documento must be a valid CPF (11 digits) or CNPJ (14 digits)")

// NormalizeDocumento reduces a CPF or CNPJ to its digits-only form and
// validates the length. This is deliberately structural only — it never
// checks the value against the Receita Federal or the X2 API (design §3,
// §6): it exists to catch an obvious typo before saving, nothing more.
func NormalizeDocumento(raw string) (string, error) {
	digits := NormalizePhone(raw) // same "keep only 0-9" logic, reused rather than duplicated
	if len(digits) != 11 && len(digits) != 14 {
		return "", ErrInvalidDocumento
	}
	return digits, nil
}
```

(add `"errors"` to the import block if not already present)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/contactutil/... -v`
Expected: PASS, including pre-existing tests in the file (no regression).

- [ ] **Step 5: Commit**

```bash
git add internal/contactutil/contactutil.go internal/contactutil/contactutil_test.go
git commit -m "feat(contactutil): add NormalizeDocumento (structural CPF/CNPJ validation)"
```

---

## Task 7: Modelo do vínculo (`SalesOpportunityXProcessLink`) + colunas na oportunidade

**Files:**
- Create: `internal/models/sales_opportunity_xprocess_link.go`
- Modify: `internal/models/sales_opportunity.go`
- Modify: `internal/database/postgres.go`

**Interfaces:**
- Produces: `models.SalesOpportunityXProcessLink{BaseModel, OrganizationID, SalesOpportunityID uuid.UUID, NumPedido, Documento string, CodEmpresa, CodVendedor, StatusXProcess *string, ValorVendido *float64, Itens JSONBArray, LastCheckedAt, FirstClosedAt, ResolvedAt *time.Time, ConsecutiveNotFound int}`.
- Modifies: `models.SalesOpportunity` gains `XProcessNumPedido`, `XProcessDocumento *string`.

- [ ] **Step 1: Write the failing test**

```go
// internal/models/sales_opportunity_xprocess_link_test.go
package models_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestSalesOpportunityXProcessLink_TableName(t *testing.T) {
	assert.Equal(t, "sales_opportunity_xprocess_links", models.SalesOpportunityXProcessLink{}.TableName())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/models/... -run TestSalesOpportunityXProcessLink -v`
Expected: FAIL — type undefined.

- [ ] **Step 3: Write the model**

```go
// internal/models/sales_opportunity_xprocess_link.go
package models

import (
	"time"

	"github.com/google/uuid"
)

// SalesOpportunityXProcessLink tracks one attempt to reconcile a
// SalesOpportunity with an X2 pedido (design §4). Deliberately NOT 1:1 with
// the opportunity: there can be several rows over time, but at most one
// with ResolvedAt == nil at any moment — see UpdateSalesOpportunityXProcessLink
// (internal/handlers) for the rule that enforces this.
type SalesOpportunityXProcessLink struct {
	BaseModel
	OrganizationID     uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`
	SalesOpportunityID uuid.UUID `gorm:"type:uuid;index;not null" json:"sales_opportunity_id"`

	// NumPedido/Documento are copied from the opportunity at registration
	// time, not read live from it — a resolved link's audit trail must
	// reflect what was actually checked, even if the opportunity's own
	// fields later point at a newer link.
	NumPedido string `gorm:"size:20;not null" json:"num_pedido"`
	Documento string `gorm:"size:14;not null" json:"documento"` // digits only, 11 (CPF) or 14 (CNPJ)

	CodEmpresa     *string  `gorm:"size:20" json:"cod_empresa,omitempty"`
	CodVendedor    *string  `gorm:"size:20" json:"cod_vendedor,omitempty"`
	// column:status_xprocess is explicit because GORM's default namer splits
	// "StatusXProcess" as "status_x_process" (verified against
	// gorm.io/gorm/schema.NamingStrategy — the lone capital X before a
	// mixed-case word still counts as its own segment). Same class of bug as
	// Contact.CPFCNPJ (commit 00e573c) and the existing
	// User.XProcessSellerCode field below, which already carries this same
	// explicit override for the same reason.
	StatusXProcess *string `gorm:"column:status_xprocess;size:20" json:"status_xprocess,omitempty"`
	ValorVendido   *float64 `gorm:"type:numeric" json:"valor_vendido,omitempty"`
	Itens          JSONBArray `gorm:"type:jsonb" json:"itens,omitempty"`

	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	// FirstClosedAt is written exactly once, on the first round the job
	// sees status=FECHADO for this link. Never overwritten afterwards —
	// the 7-day grace period (design §5) is always computed from this
	// original value, never from LastCheckedAt.
	FirstClosedAt *time.Time `json:"first_closed_at,omitempty"`
	// ConsecutiveNotFound counts JOB ROUNDS that returned 404, not calendar
	// days since registration (design §4) — resets to 0 on any 200.
	ConsecutiveNotFound int `gorm:"default:0" json:"consecutive_not_found"`
	// ResolvedAt nil means the job keeps selecting this link. See design §5's
	// resolved_at table for the exact per-status rule.
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

func (SalesOpportunityXProcessLink) TableName() string { return "sales_opportunity_xprocess_links" }
```

- [ ] **Step 4: Add the columns to SalesOpportunity**

In `internal/models/sales_opportunity.go`, near `Direcionamento`:

```go
	// XProcessNumPedido/XProcessDocumento always mirror the CURRENT (open,
	// or latest if all resolved) SalesOpportunityXProcessLink — a read
	// convenience; the link table is the source of truth and the only
	// place a full history is kept (design §4, §6).
	// column: explicit on both — GORM's default namer produces
	// "x_process_num_pedido"/"x_process_documento" (verified against
	// gorm.io/gorm/schema.NamingStrategy), not "xprocess_...". Same reason
	// User.XProcessSellerCode already carries an explicit column tag; match
	// it here instead of letting AutoMigrate create yet another
	// differently-named column for the same "xprocess" concept.
	XProcessNumPedido *string `gorm:"column:xprocess_num_pedido;size:20" json:"xprocess_num_pedido,omitempty"`
	XProcessDocumento *string `gorm:"column:xprocess_documento;size:14" json:"xprocess_documento,omitempty"`
```

- [ ] **Step 5: Register the new model for AutoMigrate**

In `internal/database/postgres.go`:

```go
		{"XProcessIntegration", &models.XProcessIntegration{}},
		{"SalesOpportunityXProcessLink", &models.SalesOpportunityXProcessLink{}},
```

- [ ] **Step 6: Run test to verify it passes, and build**

Run: `go test ./internal/models/... -v && go build ./...`
Expected: PASS, no build errors.

- [ ] **Step 7: Commit**

```bash
git add internal/models/sales_opportunity_xprocess_link.go internal/models/sales_opportunity_xprocess_link_test.go internal/models/sales_opportunity.go internal/database/postgres.go
git commit -m "feat(models): add SalesOpportunityXProcessLink + opportunity xprocess columns"
```

---

## Task 8: Agente registra o pedido (endpoints get/upsert do vínculo)

**Files:**
- Create: `internal/handlers/sales_opportunity_xprocess_link.go`
- Create: `internal/handlers/sales_opportunity_xprocess_link_test.go`
- Modify: `cmd/whatomate/main.go`

**Interfaces:**
- Consumes: `contactutil.NormalizeDocumento`, `a.loadAuthorizedSalesOpportunity`, `models.SalesOpportunityXProcessLink`.
- Produces: `(a *App) GetSalesOpportunityXProcessLink(r *fastglue.Request) error`, `(a *App) UpsertSalesOpportunityXProcessLink(r *fastglue.Request) error`.

- [ ] **Step 1: Write the failing tests**

Follows `sales_opportunities_details_test.go`'s exact conventions (same `newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)` helper, same `testutil.NewJSONRequest`/`SetAuthContext`/`req.RequestCtx.SetUserValue("id", opp.ID.String())` pattern):

```go
// internal/handlers/sales_opportunity_xprocess_link_test.go
package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestUpsertSalesOpportunityXProcessLink_CreatesLinkAndFillsContactCPF(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{
		"num_pedido": "666", "documento": "123.456.789-00",
	})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var link models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).First(&link).Error)
	assert.Equal(t, "666", link.NumPedido)
	assert.Equal(t, "12345678900", link.Documento)
	assert.Nil(t, link.ResolvedAt)

	var updatedOpp models.SalesOpportunity
	require.NoError(t, app.DB.First(&updatedOpp, "id = ?", opp.ID).Error)
	require.NotNil(t, updatedOpp.XProcessNumPedido)
	assert.Equal(t, "666", *updatedOpp.XProcessNumPedido)

	var updatedContact models.Contact
	require.NoError(t, app.DB.First(&updatedContact, "id = ?", contact.ID).Error)
	assert.Equal(t, "12345678900", updatedContact.CPFCNPJ)
}

func TestUpsertSalesOpportunityXProcessLink_DoesNotOverwriteExistingContactCPF(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	// Column is physically "cpfcnpj" (no underscore) — see the fix note in
	// UpsertSalesOpportunityXProcessLink below; get this wrong here and the
	// test would silently pass against a column the handler never touches.
	require.NoError(t, app.DB.Model(contact).Update("cpfcnpj", "99999999999").Error)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "1", "documento": "12345678900"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(req))

	var updatedContact models.Contact
	require.NoError(t, app.DB.First(&updatedContact, "id = ?", contact.ID).Error)
	assert.Equal(t, "99999999999", updatedContact.CPFCNPJ, "must not clobber a CPF the contact already had")
}

func TestUpsertSalesOpportunityXProcessLink_InvalidDocumentoRejected(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "666", "documento": "123"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(req))
	testutil.AssertErrorResponse(t, req, fasthttp.StatusBadRequest, "")

	var count int64
	app.DB.Model(&models.SalesOpportunityXProcessLink{}).Where("sales_opportunity_id = ?", opp.ID).Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestUpsertSalesOpportunityXProcessLink_UpdatesOpenLinkInPlace(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	first := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "111", "documento": "12345678900"})
	testutil.SetAuthContext(first, org.ID, agent.ID)
	first.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(first))

	second := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "222", "documento": "98765432100"})
	testutil.SetAuthContext(second, org.ID, agent.ID)
	second.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(second))

	var links []models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Find(&links).Error)
	require.Len(t, links, 1, "editing an unresolved link must update it in place, not create a second row")
	assert.Equal(t, "222", links[0].NumPedido)
}

func TestUpsertSalesOpportunityXProcessLink_ResolvedLinkIsImmutable_CreatesNewRow(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	first := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "111", "documento": "12345678900"})
	testutil.SetAuthContext(first, org.ID, agent.ID)
	first.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(first))

	now := time.Now()
	require.NoError(t, app.DB.Model(&models.SalesOpportunityXProcessLink{}).
		Where("sales_opportunity_id = ?", opp.ID).Update("resolved_at", now).Error)

	second := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "222", "documento": "98765432100"})
	testutil.SetAuthContext(second, org.ID, agent.ID)
	second.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(second))

	var links []models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Order("created_at asc").Find(&links).Error)
	require.Len(t, links, 2, "a resolved link must never be overwritten — a new registration creates a new row")
	assert.Equal(t, "111", links[0].NumPedido)
	assert.NotNil(t, links[0].ResolvedAt, "the original resolved link must stay untouched")
	assert.Equal(t, "222", links[1].NumPedido)
	assert.Nil(t, links[1].ResolvedAt)
}

func TestGetSalesOpportunityXProcessLink_ReturnsPendingReviewFlag(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	create := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "666", "documento": "12345678900"})
	testutil.SetAuthContext(create, org.ID, agent.ID)
	create.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(create))

	require.NoError(t, app.DB.Model(&models.SalesOpportunityXProcessLink{}).
		Where("sales_opportunity_id = ?", opp.ID).Update("consecutive_not_found", 5).Error)

	get := testutil.NewGETRequest(t)
	testutil.SetAuthContext(get, org.ID, agent.ID)
	get.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.GetSalesOpportunityXProcessLink(get))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(get))

	var resp struct {
		Data struct {
			NumPedido     string `json:"num_pedido"`
			PendingReview bool   `json:"pending_review"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(get), &resp))
	assert.True(t, resp.Data.PendingReview)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/handlers/... -run TestUpsertSalesOpportunityXProcessLink -run TestGetSalesOpportunityXProcessLink -v`
Expected: FAIL — handlers undefined. (Note: `-run` only accepts one pattern; run each test function name separately, or use `-run 'TestUpsertSalesOpportunityXProcessLink|TestGetSalesOpportunityXProcessLink'`.)

- [ ] **Step 3: Write the handlers**

```go
// internal/handlers/sales_opportunity_xprocess_link.go
package handlers

import (
	"time"

	"github.com/shridarpatil/whatomate/internal/contactutil"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

type upsertSalesOpportunityXProcessLinkRequest struct {
	NumPedido string `json:"num_pedido"`
	Documento string `json:"documento"`
}

// UpsertSalesOpportunityXProcessLink registers (or edits, while still
// unresolved) the X2 pedido number + documento for an open opportunity
// (design §6). Never calls the X2 API — the daily reconciliation job does
// that (design's core D-1 constraint, §1).
func (a *App) UpsertSalesOpportunityXProcessLink(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionWrite)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	if opp.Status != models.SalesOpportunityStatusAberta {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can be edited", nil, "")
	}

	var req upsertSalesOpportunityXProcessLinkRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.NumPedido == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "num_pedido is required", nil, "")
	}
	documento, err := contactutil.NormalizeDocumento(req.Documento)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}

	// Find this opportunity's open (unresolved) link, if any — design §4/§6:
	// while unresolved, editing updates the same row; once resolved, a new
	// registration must create a new row instead (never overwrite).
	var openLink models.SalesOpportunityXProcessLink
	hasOpenLink := a.DB.Where("sales_opportunity_id = ? AND resolved_at IS NULL", opp.ID).
		First(&openLink).Error == nil

	if hasOpenLink {
		if err := a.DB.Model(&openLink).Updates(map[string]any{
			"num_pedido":            req.NumPedido,
			"documento":             documento,
			"consecutive_not_found": 0,
			"last_checked_at":       nil,
		}).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update link", nil, "")
		}
	} else {
		newLink := models.SalesOpportunityXProcessLink{
			OrganizationID:     orgID,
			SalesOpportunityID: opp.ID,
			NumPedido:          req.NumPedido,
			Documento:          documento,
		}
		if err := a.DB.Create(&newLink).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create link", nil, "")
		}
	}

	if err := a.DB.Model(&models.SalesOpportunity{}).Where("id = ?", opp.ID).Updates(map[string]any{
		"xprocess_num_pedido": req.NumPedido,
		"xprocess_documento":  documento,
	}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update opportunity", nil, "")
	}

	// Fill Contact.CPFCNPJ only if empty — never clobber a value it already
	// had (design §3: "Preenchido a partir do documento informado, se
	// estiver vazio"). Column is physically "cpfcnpj" (no underscore): GORM's
	// default snake_case for the all-caps Go field CPFCNPJ, with no explicit
	// `column:` override on the model. This already broke contact saves once
	// in this exact codebase (see commit 00e573c, "fix(contacts): use the
	// real cpfcnpj column name in map-based updates") — do not "fix" this
	// back to cpf_cnpj, it is deliberately the physical column name.
	a.DB.Model(&models.Contact{}).
		Where("id = ? AND (cpfcnpj IS NULL OR cpfcnpj = '')", opp.ContactID).
		Update("cpfcnpj", documento)

	a.DB.First(opp, "id = ?", opp.ID)
	return r.SendEnvelope(opp)
}

type salesOpportunityXProcessLinkResponse struct {
	NumPedido      string     `json:"num_pedido"`
	Documento      string     `json:"documento"`
	StatusXProcess *string    `json:"status_xprocess,omitempty"`
	ValorVendido   *float64   `json:"valor_vendido,omitempty"`
	LastCheckedAt  *time.Time `json:"last_checked_at,omitempty"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	// PendingReview mirrors the design's §5/§8 rule: 5+ consecutive 404
	// rounds, still unresolved. Computed here, not stored — the wording
	// shown to the agent must say "not located", never "invalid" (design's
	// global constraints).
	PendingReview bool `json:"pending_review"`
}

// xprocessNotFoundReviewThreshold is the number of consecutive job rounds
// with a 404 before a link surfaces for agent review (design §3, §5, §8).
const xprocessNotFoundReviewThreshold = 5

// GetSalesOpportunityXProcessLink returns the opportunity's current link —
// the open one if there is one, otherwise the most recently resolved one.
func (a *App) GetSalesOpportunityXProcessLink(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionRead)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}

	var link models.SalesOpportunityXProcessLink
	if err := a.DB.Where("sales_opportunity_id = ?", opp.ID).
		Order("resolved_at IS NULL DESC, created_at DESC").
		First(&link).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "No X2 link registered for this opportunity", nil, "")
	}

	return r.SendEnvelope(salesOpportunityXProcessLinkResponse{
		NumPedido: link.NumPedido, Documento: link.Documento,
		StatusXProcess: link.StatusXProcess, ValorVendido: link.ValorVendido,
		LastCheckedAt: link.LastCheckedAt, ResolvedAt: link.ResolvedAt,
		PendingReview: link.ResolvedAt == nil && link.ConsecutiveNotFound >= xprocessNotFoundReviewThreshold,
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/handlers/... -run 'TestUpsertSalesOpportunityXProcessLink|TestGetSalesOpportunityXProcessLink' -v`
Expected: PASS, all 6 tests green.

- [ ] **Step 5: Register routes**

In `cmd/whatomate/main.go`, next to the other sales-opportunities routes:

```go
	g.GET("/api/sales-opportunities/{id}/xprocess-link", app.GetSalesOpportunityXProcessLink)
	g.PUT("/api/sales-opportunities/{id}/xprocess-link", app.UpsertSalesOpportunityXProcessLink)
```

- [ ] **Step 6: Build and run full handlers suite**

Run: `go build ./... && go test ./internal/handlers/... -run SalesOpportunity -v`
Expected: PASS, no regressions in existing sales opportunity tests.

- [ ] **Step 7: Commit**

```bash
git add internal/handlers/sales_opportunity_xprocess_link.go internal/handlers/sales_opportunity_xprocess_link_test.go cmd/whatomate/main.go
git commit -m "feat(sales): agent registers X2 pedido number + documento on an opportunity"
```

---

## Task 9: Formulário do agente na oportunidade (frontend)

**Files:**
- Create: `frontend/src/components/sales/XProcessLinkForm.vue`
- Modify: `frontend/src/services/api.ts`
- Modify: `frontend/src/i18n/locales/en.json`, `frontend/src/i18n/locales/pt-BR.json`
- Modify: whichever component renders the opportunity detail (locate via `grep -rn "estimated_value" frontend/src/components/sales frontend/src/views/sales` and mount `XProcessLinkForm` next to that same inline-edit area)

**Interfaces:**
- Consumes: `GET/PUT /api/sales-opportunities/{id}/xprocess-link` (Task 8).

- [ ] **Step 1: Add the API service functions**

In `frontend/src/services/api.ts`:

```typescript
export interface SalesOpportunityXProcessLink {
  num_pedido: string
  documento: string
  status_xprocess?: string
  valor_vendido?: number
  last_checked_at?: string
  resolved_at?: string
  pending_review: boolean
}

export const salesOpportunityXProcessLinkService = {
  get: (opportunityId: string) =>
    api.get<{ data: SalesOpportunityXProcessLink }>(`/sales-opportunities/${opportunityId}/xprocess-link`),
  upsert: (opportunityId: string, payload: { num_pedido: string; documento: string }) =>
    api.put(`/sales-opportunities/${opportunityId}/xprocess-link`, payload),
}
```

- [ ] **Step 2: Add i18n keys**

`en.json`, new `xprocessLink` section:
```json
  "xprocessLink": {
    "title": "X2 order",
    "numPedido": "Order number",
    "documento": "Customer CPF/CNPJ",
    "register": "Register",
    "registered": "Registered — automatic confirmation runs tomorrow morning.",
    "pendingReview": "Order not yet located in X2 after 5 checks — the number or document may need a second look.",
    "status": "X2 status"
  },
```
`pt-BR.json`:
```json
  "xprocessLink": {
    "title": "Pedido X2",
    "numPedido": "Número do pedido",
    "documento": "CPF/CNPJ do cliente",
    "register": "Registrar",
    "registered": "Registrado — a confirmação automática roda amanhã de manhã.",
    "pendingReview": "Pedido ainda não localizado no X2 após 5 consultas — vale conferir número ou documento.",
    "status": "Status no X2"
  },
```

- [ ] **Step 3: Write the component**

```vue
<!-- frontend/src/components/sales/XProcessLinkForm.vue -->
<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { salesOpportunityXProcessLinkService, type SalesOpportunityXProcessLink } from '@/services/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

const props = defineProps<{ opportunityId: string; editable: boolean }>()

const { t } = useI18n()
const link = ref<SalesOpportunityXProcessLink | null>(null)
const numPedido = ref('')
const documento = ref('')
const saving = ref(false)

async function load() {
  try {
    const res = await salesOpportunityXProcessLinkService.get(props.opportunityId)
    link.value = res.data.data
  } catch {
    link.value = null
  }
}

async function register() {
  if (!numPedido.value || !documento.value) return
  saving.value = true
  try {
    await salesOpportunityXProcessLinkService.upsert(props.opportunityId, {
      num_pedido: numPedido.value, documento: documento.value,
    })
    toast.success(t('xprocessLink.registered'))
    numPedido.value = ''
    documento.value = ''
    await load()
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-2 rounded-lg border p-3">
    <h4 class="text-sm font-medium">{{ t('xprocessLink.title') }}</h4>

    <div v-if="link" class="text-sm text-muted-foreground">
      <p>{{ t('xprocessLink.numPedido') }}: {{ link.num_pedido }}</p>
      <p v-if="link.status_xprocess">{{ t('xprocessLink.status') }}: {{ link.status_xprocess }}</p>
      <p v-if="link.pending_review" class="text-amber-600">{{ t('xprocessLink.pendingReview') }}</p>
    </div>

    <div v-if="editable && (!link || link.resolved_at)" class="flex flex-col gap-2 sm:flex-row">
      <div class="flex-1 space-y-1">
        <Label>{{ t('xprocessLink.numPedido') }}</Label>
        <Input v-model="numPedido" />
      </div>
      <div class="flex-1 space-y-1">
        <Label>{{ t('xprocessLink.documento') }}</Label>
        <Input v-model="documento" />
      </div>
      <Button class="self-end" :disabled="saving || !numPedido || !documento" @click="register">
        {{ t('xprocessLink.register') }}
      </Button>
    </div>
  </div>
</template>
```

The `editable && (!link || link.resolved_at)` guard on the form mirrors Task 8's server-side rule exactly: while there is an unresolved link, re-registering hits the same "update in place" path (so this same form doubles as the edit form for an open link — the input fields simply start pre-filled instead of empty; leave that pre-fill wiring to a follow-up polish pass, not required for this task's test coverage). Once resolved, the form reappears to create a new link.

- [ ] **Step 4: Mount the component in the opportunity view**

Run `grep -rn "estimated_value" frontend/src/components/sales frontend/src/views/sales` to find the exact file/section that renders the inline-edit fields for an open opportunity (per Task 8's design note, this is the same screen). Add, right after that block:

```vue
<XProcessLinkForm :opportunity-id="opportunity.id" :editable="opportunity.status === 'aberta'" />
```

Add the import at the top of that file: `import XProcessLinkForm from '@/components/sales/XProcessLinkForm.vue'`.

- [ ] **Step 5: Verify it builds and lints**

Run: `cd frontend && npm run typecheck && npx eslint src/components/sales/XProcessLinkForm.vue src/services/api.ts --ext .vue,.ts && npm run build`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/sales/XProcessLinkForm.vue frontend/src/services/api.ts frontend/src/i18n/locales/en.json frontend/src/i18n/locales/pt-BR.json
git add -u  # picks up the opportunity view file modified in Step 4
git commit -m "feat(sales): X2 pedido registration form on the opportunity"
```

---

## Task 10: Máquina de estados da conciliação (`reconcileXProcessLink`)

Este é o núcleo do job — testado sem depender do `XProcessReconciler`/ticker (Task 11 cobre isso).

**Files:**
- Create: `internal/handlers/xprocess_reconciler.go`
- Create: `internal/handlers/xprocess_reconciler_test.go`

**Interfaces:**
- Consumes: `xprocess.Client`, `xprocess.ConsultarPedido`, `xprocess.SummarizePedido`, `xprocess.ErrPedidoNaoEncontrado`.
- Produces: `(a *App) reconcileXProcessLink(client *xprocess.Client, apiKey string, link *models.SalesOpportunityXProcessLink)`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/handlers/xprocess_reconciler_test.go
package handlers_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/xprocess"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeXProcessServer returns a canned /api/pedido response for every request.
func fakeXProcessServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func pedidoJSON(status string) string {
	return fmt.Sprintf(`{"ok":true,"pedido":[
		{"cod_empresa":"7","num_pedido":"666","cod_cliente":"9446","cod_vendedor":"2586","status":%q,"total":"100,0","data_venda":null}
	]}`, status)
}

func TestReconcileXProcessLink_NotFound_IncrementsCounter(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	link := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusNotFound, `{"detail":"Pedido nao encontrado"}`)
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	assert.Equal(t, 1, got.ConsecutiveNotFound)
	assert.Nil(t, got.ResolvedAt)

	var oppAfter models.SalesOpportunity
	require.NoError(t, app.DB.First(&oppAfter, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusAberta, oppAfter.Status)
}

func TestReconcileXProcessLink_Separacao_ConvertsOpenOpportunity(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	link := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("SEPARACAO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var oppAfter models.SalesOpportunity
	require.NoError(t, app.DB.First(&oppAfter, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusConvertida, oppAfter.Status)
	require.NotNil(t, oppAfter.ConversionSource)
	assert.Equal(t, models.SalesConversionSourceXProcess, *oppAfter.ConversionSource)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, models.SalesOpportunityEventConverted, events[0].Type)
	assert.Equal(t, models.SalesOpportunityEventSourceXProcess, events[0].Source)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	assert.Nil(t, got.ResolvedAt, "SEPARACAO must keep the job checking (design §5)")
	require.NotNil(t, got.ValorVendido)
	assert.InDelta(t, 100.0, *got.ValorVendido, 0.001)
}

func TestReconcileXProcessLink_Separacao_DoesNotReconvertAlreadyConverted(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Updates(map[string]any{
		"status": models.SalesOpportunityStatusConvertida, "conversion_source": models.SalesConversionSourceManual,
	}).Error)
	link := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("SEPARACAO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Find(&events).Error)
	assert.Empty(t, events, "an already-converted opportunity must not get a duplicate converted event")

	var oppAfter models.SalesOpportunity
	require.NoError(t, app.DB.First(&oppAfter, "id = ?", opp.ID).Error)
	require.NotNil(t, oppAfter.ConversionSource)
	assert.Equal(t, models.SalesConversionSourceManual, *oppAfter.ConversionSource, "manual conversion must not be overwritten")
}

func TestReconcileXProcessLink_Fechado_FirstTime_SetsFirstClosedAtButStaysUnresolved(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	link := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("FECHADO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	require.NotNil(t, got.FirstClosedAt)
	assert.Nil(t, got.ResolvedAt, "resolved_at must NOT be set on the same round that first sees FECHADO — "+
		"the job must keep checking through the 7-day grace window")

	var oppAfter models.SalesOpportunity
	require.NoError(t, app.DB.First(&oppAfter, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusConvertida, oppAfter.Status)
}

func TestReconcileXProcessLink_Fechado_WithinGracePeriod_StaysUnresolved(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Updates(map[string]any{
		"status": models.SalesOpportunityStatusConvertida, "conversion_source": models.SalesConversionSourceXProcess,
	}).Error)
	threeDaysAgo := time.Now().Add(-3 * 24 * time.Hour)
	link := models.SalesOpportunityXProcessLink{
		OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900",
		FirstClosedAt: &threeDaysAgo,
	}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("FECHADO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	assert.Nil(t, got.ResolvedAt, "3 days after FECHADO is still inside the 7-day grace window")
	assert.WithinDuration(t, threeDaysAgo, *got.FirstClosedAt, time.Second, "first_closed_at must never be rewritten on a later round")
}

func TestReconcileXProcessLink_Fechado_AfterGracePeriod_Resolves(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Updates(map[string]any{
		"status": models.SalesOpportunityStatusConvertida, "conversion_source": models.SalesConversionSourceXProcess,
	}).Error)
	eightDaysAgo := time.Now().Add(-8 * 24 * time.Hour)
	link := models.SalesOpportunityXProcessLink{
		OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900",
		FirstClosedAt: &eightDaysAgo,
	}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("FECHADO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	require.NotNil(t, got.ResolvedAt, "8 days after first_closed_at is past the 7-day grace window")
}

func TestReconcileXProcessLink_Cancelado_NeverConverted_MarksLost(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	link := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("CANCELADO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var oppAfter models.SalesOpportunity
	require.NoError(t, app.DB.First(&oppAfter, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusPerdida, oppAfter.Status)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	require.NotNil(t, got.ResolvedAt)
}

func TestReconcileXProcessLink_Cancelado_AlreadyConverted_MarksCancelledPreservingHistory(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Updates(map[string]any{
		"status": models.SalesOpportunityStatusConvertida, "conversion_source": models.SalesConversionSourceXProcess,
	}).Error)
	require.NoError(t, app.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: org.ID, SalesOpportunityID: opp.ID,
		Type: models.SalesOpportunityEventConverted, Source: models.SalesOpportunityEventSourceXProcess,
	}).Error)
	twoDaysAgo := time.Now().Add(-2 * 24 * time.Hour)
	link := models.SalesOpportunityXProcessLink{
		OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900",
		FirstClosedAt: &twoDaysAgo, // still well inside the 7-day grace window
	}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("CANCELADO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var oppAfter models.SalesOpportunity
	require.NoError(t, app.DB.First(&oppAfter, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusCancelada, oppAfter.Status)
	require.NotNil(t, oppAfter.CancelledAt)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Order("created_at asc").Find(&events).Error)
	require.Len(t, events, 2, "the original converted event must be preserved, a cancelled event added")
	assert.Equal(t, models.SalesOpportunityEventConverted, events[0].Type)
	assert.Equal(t, models.SalesOpportunityEventCancelled, events[1].Type)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	require.NotNil(t, got.ResolvedAt, "CANCELADO within the grace window must resolve immediately, not wait for 7 days")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/handlers/... -run TestReconcileXProcessLink -v`
Expected: FAIL — `ReconcileXProcessLinkForTest` undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/handlers/xprocess_reconciler.go
package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/xprocess"
)

// xprocessClosedGracePeriod is how long the job keeps checking a link after
// the first time it sees FECHADO, in case the pedido gets cancelled after
// invoicing (design §3, §5).
const xprocessClosedGracePeriod = 7 * 24 * time.Hour

// reconcileXProcessLink checks one pending link against X2 and applies the
// design's §5 outcome rules. Never called directly by an HTTP handler —
// only by RunXProcessReconciliation (Task 11) and by tests via the small
// exported wrapper below.
func (a *App) reconcileXProcessLink(client *xprocess.Client, apiKey string, link *models.SalesOpportunityXProcessLink) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now()

	items, err := client.ConsultarPedido(ctx, apiKey, link.NumPedido, link.Documento)
	if errors.Is(err, xprocess.ErrPedidoNaoEncontrado) {
		a.DB.Model(link).Updates(map[string]any{
			"last_checked_at":       now,
			"consecutive_not_found": link.ConsecutiveNotFound + 1,
		})
		return
	}
	if err != nil {
		a.Log.Error("xprocess reconciliation: request failed", "link_id", link.ID, "num_pedido", link.NumPedido, "error", err)
		a.DB.Model(link).Update("last_checked_at", now)
		return
	}

	resumo, err := xprocess.SummarizePedido(items)
	if err != nil {
		a.Log.Error("xprocess reconciliation: failed to summarize pedido", "link_id", link.ID, "error", err)
		a.DB.Model(link).Update("last_checked_at", now)
		return
	}

	var opp models.SalesOpportunity
	if err := a.DB.First(&opp, "id = ?", link.SalesOpportunityID).Error; err != nil {
		a.Log.Error("xprocess reconciliation: opportunity not found", "link_id", link.ID, "opportunity_id", link.SalesOpportunityID, "error", err)
		return
	}

	itensJSON, err := toJSONBArray(resumo.Itens)
	if err != nil {
		a.Log.Error("xprocess reconciliation: failed to encode itens", "link_id", link.ID, "error", err)
	}

	updates := map[string]any{
		"last_checked_at":       now,
		"consecutive_not_found": 0,
		"cod_empresa":           resumo.CodEmpresa,
		"cod_vendedor":          resumo.CodVendedor,
		"status_xprocess":       resumo.Status,
		"valor_vendido":         resumo.ValorTotal,
		"itens":                 itensJSON,
	}

	switch resumo.Status {
	case "SEPARACAO", "SEPARADO":
		if opp.Status == models.SalesOpportunityStatusAberta {
			a.convertXProcessOpportunity(&opp, link)
		}
		// resolved_at stays nil — keep checking until FECHADO or CANCELADO.

	case "FECHADO":
		if opp.Status == models.SalesOpportunityStatusAberta {
			a.convertXProcessOpportunity(&opp, link)
		}
		firstClosedAt := link.FirstClosedAt
		if firstClosedAt == nil {
			updates["first_closed_at"] = now
			firstClosedAt = &now
		}
		if now.After(firstClosedAt.Add(xprocessClosedGracePeriod)) {
			updates["resolved_at"] = now
		}

	case "CANCELADO":
		if opp.Status == models.SalesOpportunityStatusConvertida {
			a.cancelXProcessOpportunity(&opp, link)
		} else if opp.Status == models.SalesOpportunityStatusAberta {
			a.loseXProcessOpportunity(&opp, link)
		}
		updates["resolved_at"] = now
	}

	a.DB.Model(link).Updates(updates)
}

// convertXProcessOpportunity mirrors ConvertSalesOpportunity's own DB
// writes exactly, but with source=xprocess and no HTTP actor — this is a
// background job, not a user request.
func (a *App) convertXProcessOpportunity(opp *models.SalesOpportunity, link *models.SalesOpportunityXProcessLink) {
	now := time.Now()
	source := models.SalesConversionSourceXProcess
	result := a.DB.Model(&models.SalesOpportunity{}).
		Where("id = ? AND status = ?", opp.ID, models.SalesOpportunityStatusAberta).
		Updates(map[string]any{
			"status": models.SalesOpportunityStatusConvertida,
			"conversion_source": source, "converted_at": now,
			"sla_breached": false,
		})
	if result.Error != nil || result.RowsAffected == 0 {
		return // already converted (manually or by a concurrent run) — no event, no duplicate
	}
	a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: link.OrganizationID, SalesOpportunityID: opp.ID,
		Type: models.SalesOpportunityEventConverted, Source: models.SalesOpportunityEventSourceXProcess,
	})
	opp.Status = models.SalesOpportunityStatusConvertida
}

// loseXProcessOpportunity marks an opportunity that never converted as lost
// because X2 shows its pedido cancelled (design §5).
func (a *App) loseXProcessOpportunity(opp *models.SalesOpportunity, link *models.SalesOpportunityXProcessLink) {
	now := time.Now()
	reason := models.SalesLossReasonOutro
	result := a.DB.Model(&models.SalesOpportunity{}).
		Where("id = ? AND status = ?", opp.ID, models.SalesOpportunityStatusAberta).
		Updates(map[string]any{
			"status": models.SalesOpportunityStatusPerdida,
			"loss_reason": reason, "loss_notes": "Cancelado no X2 (pedido " + link.NumPedido + ")",
			"lost_at": now, "sla_breached": false,
		})
	if result.Error != nil || result.RowsAffected == 0 {
		return
	}
	a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: link.OrganizationID, SalesOpportunityID: opp.ID,
		Type: models.SalesOpportunityEventLost, Source: models.SalesOpportunityEventSourceXProcess,
	})
}

// cancelXProcessOpportunity marks an already-converted opportunity as
// cancelled without touching the original converted event (design §3, §5 —
// history must show "converted on X, cancelled on Y", never erase the sale
// existed).
func (a *App) cancelXProcessOpportunity(opp *models.SalesOpportunity, link *models.SalesOpportunityXProcessLink) {
	now := time.Now()
	result := a.DB.Model(&models.SalesOpportunity{}).
		Where("id = ? AND status = ?", opp.ID, models.SalesOpportunityStatusConvertida).
		Updates(map[string]any{"status": models.SalesOpportunityStatusCancelada, "cancelled_at": now})
	if result.Error != nil || result.RowsAffected == 0 {
		return
	}
	a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: link.OrganizationID, SalesOpportunityID: opp.ID,
		Type: models.SalesOpportunityEventCancelled, Source: models.SalesOpportunityEventSourceXProcess,
	})
}

// toJSONBArray round-trips a typed slice through JSON into models.JSONBArray
// (which is itself []any) — simplest correct way to store xprocess.PedidoItem
// values in a jsonb column without hand-rolling a second marshaler.
func toJSONBArray(items []xprocess.PedidoItem) (models.JSONBArray, error) {
	raw, err := jsonMarshal(items)
	if err != nil {
		return nil, err
	}
	var out []any
	if err := jsonUnmarshal(raw, &out); err != nil {
		return nil, err
	}
	return models.JSONBArray(out), nil
}

// ReconcileXProcessLinkForTest exposes reconcileXProcessLink to tests in
// this package (handlers_test) without widening the real, unexported API
// surface used by production code (RunXProcessReconciliation, Task 11).
func (a *App) ReconcileXProcessLinkForTest(client *xprocess.Client, apiKey string, link *models.SalesOpportunityXProcessLink) {
	a.reconcileXProcessLink(client, apiKey, link)
}
```

Add `"encoding/json"` to the imports and use it directly instead of inventing `jsonMarshal`/`jsonUnmarshal` wrapper names — replace the two calls above with `json.Marshal(items)` / `json.Unmarshal(raw, &out)` and import `"encoding/json"` (this plan spelled them out as separate names only to make the diff obvious; the real file must use the standard library directly, not custom-named wrappers).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/handlers/... -run TestReconcileXProcessLink -v`
Expected: PASS, all 8 tests green — pay special attention to `TestReconcileXProcessLink_Fechado_FirstTime_SetsFirstClosedAtButStaysUnresolved` and `TestReconcileXProcessLink_Cancelado_AlreadyConverted_MarksCancelledPreservingHistory`, the two tests that directly guard the bug the design review caught.

- [ ] **Step 5: Build**

Run: `go build ./... && go vet ./internal/handlers/...`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/xprocess_reconciler.go internal/handlers/xprocess_reconciler_test.go
git commit -m "feat(sales): X2 reconciliation state machine (reconcileXProcessLink)"
```

---

## Task 11: Loop diário (`XProcessReconciler`) + wiring

**Files:**
- Modify: `internal/handlers/xprocess_reconciler.go` (adiciona `RunXProcessReconciliation` + `XProcessReconciler`)
- Modify: `internal/handlers/xprocess_reconciler_test.go`
- Modify: `cmd/whatomate/main.go`

**Interfaces:**
- Consumes: `reconcileXProcessLink` (Task 10).
- Produces: `(a *App) RunXProcessReconciliation()`, `handlers.NewXProcessReconciler(app *App, interval time.Duration, triggerHour int) *XProcessReconciler`, `(*XProcessReconciler) Start(ctx context.Context)`, `(*XProcessReconciler) Stop()`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/handlers/xprocess_reconciler_test.go`:

```go
func TestRunXProcessReconciliation_ProcessesOnlyPendingLinksForActiveIntegrations(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	openOpp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	openLink := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: openOpp.ID, NumPedido: "1", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&openLink).Error)

	resolvedOpp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	resolvedAt := time.Now()
	resolvedLink := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: resolvedOpp.ID, NumPedido: "2", Documento: "12345678900", ResolvedAt: &resolvedAt}
	require.NoError(t, app.DB.Create(&resolvedLink).Error)

	var requestedNums []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NumeroPedido string `json:"numero_pedido"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		requestedNums = append(requestedNums, body.NumeroPedido)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pedidoJSON("SEPARACAO")))
	}))
	defer srv.Close()

	integ := models.XProcessIntegration{OrganizationID: org.ID, BaseURL: srv.URL, APIKey: "the-key", IsActive: true}
	require.NoError(t, integ.EncryptSecrets(app.Config.App.EncryptionKey))
	require.NoError(t, app.DB.Create(&integ).Error)

	app.RunXProcessReconciliation()

	assert.Equal(t, []string{"1"}, requestedNums, "only the unresolved link must be checked")
}

func TestRunXProcessReconciliation_SkipsInactiveIntegration(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	link := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "1", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&link).Error)

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()

	integ := models.XProcessIntegration{OrganizationID: org.ID, BaseURL: srv.URL, APIKey: "the-key", IsActive: false}
	require.NoError(t, integ.EncryptSecrets(app.Config.App.EncryptionKey))
	require.NoError(t, app.DB.Create(&integ).Error)

	app.RunXProcessReconciliation()
	assert.False(t, called, "an inactive integration must never be queried")
}

func TestXProcessReconciler_RunsOnceAtTriggerHourNotBefore(t *testing.T) {
	app := newTestApp(t)
	var runs int
	reconciler := NewXProcessReconcilerForTest(app, func() { runs++ })

	reconciler.MaybeRunForTest(time.Date(2026, 9, 27, 1, 59, 0, 0, time.UTC))
	assert.Equal(t, 0, runs, "must not run before the trigger hour")

	reconciler.MaybeRunForTest(time.Date(2026, 9, 27, 2, 5, 0, 0, time.UTC))
	assert.Equal(t, 1, runs)

	reconciler.MaybeRunForTest(time.Date(2026, 9, 27, 3, 0, 0, 0, time.UTC))
	assert.Equal(t, 1, runs, "must not run twice on the same calendar day")

	reconciler.MaybeRunForTest(time.Date(2026, 9, 28, 2, 5, 0, 0, time.UTC))
	assert.Equal(t, 2, runs, "must run again the next day")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/handlers/... -run 'TestRunXProcessReconciliation|TestXProcessReconciler' -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Write the implementation**

Append to `internal/handlers/xprocess_reconciler.go`:

```go
// RunXProcessReconciliation is one full sweep: every active
// XProcessIntegration, every one of that organization's unresolved links.
// Called once daily by XProcessReconciler.Start, and directly by the
// backfill/manual-trigger path if one is ever added.
func (a *App) RunXProcessReconciliation() {
	var integrations []models.XProcessIntegration
	if err := a.DB.Where("is_active = ?", true).Find(&integrations).Error; err != nil {
		a.Log.Error("xprocess reconciliation: failed to load integrations", "error", err)
		return
	}
	for i := range integrations {
		integ := integrations[i]
		integ.DecryptSecrets(a.Config.App.EncryptionKey)

		var links []models.SalesOpportunityXProcessLink
		if err := a.DB.Where("organization_id = ? AND resolved_at IS NULL", integ.OrganizationID).Find(&links).Error; err != nil {
			a.Log.Error("xprocess reconciliation: failed to load pending links", "org_id", integ.OrganizationID, "error", err)
			continue
		}
		if len(links) == 0 {
			continue
		}

		client := xprocess.New(a.Log, integ.BaseURL)
		for j := range links {
			a.reconcileXProcessLink(client, integ.APIKey, &links[j])
		}
		a.Log.Info("xprocess reconciliation: organization done", "org_id", integ.OrganizationID, "links_checked", len(links))
	}
}

// XProcessReconciler runs RunXProcessReconciliation once per calendar day,
// starting at triggerHour. Mirrors SLAProcessor's ticker+goroutine shape
// (internal/handlers/sla_processor.go) rather than adding a cron
// dependency — the interval just needs to be short enough that the daily
// run starts promptly after triggerHour, nothing fancier.
//
// lastRunDate is in-memory only: a server restart between triggerHour and
// midnight will run the sweep again that day. Harmless — reconciliation is
// naturally idempotent per link (an already-resolved link is never
// re-selected, and re-checking an unresolved one just re-fetches the same
// D-1 snapshot) — not worth a persisted "did we run today" ledger.
// ponytail: in-memory day tracking, move to a persisted marker if this ever
// runs across multiple server instances (it doesn't, as of this entrega).
type XProcessReconciler struct {
	app         *App
	interval    time.Duration
	triggerHour int
	runFunc     func()
	lastRunDate string
	stopCh      chan struct{}
}

// NewXProcessReconciler creates a reconciler that calls
// app.RunXProcessReconciliation once per day, on the first tick at or after
// triggerHour (server local time).
func NewXProcessReconciler(app *App, interval time.Duration, triggerHour int) *XProcessReconciler {
	r := &XProcessReconciler{app: app, interval: interval, triggerHour: triggerHour, stopCh: make(chan struct{})}
	r.runFunc = app.RunXProcessReconciliation
	return r
}

func (p *XProcessReconciler) Start(ctx context.Context) {
	p.app.Log.Info("XProcess reconciler started", "interval", p.interval, "trigger_hour", p.triggerHour)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.maybeRun(time.Now())
		}
	}
}

func (p *XProcessReconciler) Stop() {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
}

func (p *XProcessReconciler) maybeRun(now time.Time) {
	today := now.Format("2006-01-02")
	if now.Hour() < p.triggerHour || p.lastRunDate == today {
		return
	}
	p.lastRunDate = today
	p.runFunc()
}

// NewXProcessReconcilerForTest and MaybeRunForTest let
// TestXProcessReconciler_RunsOnceAtTriggerHourNotBefore drive maybeRun with
// fixed timestamps instead of waiting on a real ticker, and substitute a
// counting stub for the real (DB-hitting) RunXProcessReconciliation.
func NewXProcessReconcilerForTest(app *App, runFunc func()) *XProcessReconciler {
	r := NewXProcessReconciler(app, time.Minute, 2)
	r.runFunc = runFunc
	return r
}

func (p *XProcessReconciler) MaybeRunForTest(now time.Time) { p.maybeRun(now) }
```

Add `"context"` (already imported for the timeout in `reconcileXProcessLink`) — no new import needed there. `xprocess` package is already imported.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/handlers/... -run 'TestRunXProcessReconciliation|TestXProcessReconciler' -v`
Expected: PASS, all 3 tests green.

- [ ] **Step 5: Wire into main.go**

In `cmd/whatomate/main.go`, next to the SLA processor startup:

```go
	// Start XProcess reconciler (checks once daily, first tick after 2am)
	xprocessReconciler := handlers.NewXProcessReconciler(app, 15*time.Minute, 2)
	xprocessCtx, xprocessCancel := context.WithCancel(context.Background())
	go xprocessReconciler.Start(xprocessCtx)
	lo.Info("XProcess reconciler started")
```

Add the matching `xprocessCancel()` call wherever `slaCancel()` is called during graceful shutdown, so both processors stop together.

- [ ] **Step 6: Build and run the full handlers package**

Run: `go build ./... && go test ./internal/handlers/... -v 2>&1 | tail -40`
Expected: PASS across the package, no regressions (the SLA-timestamp flake noted in project memory, if it appears, is pre-existing and unrelated — confirm via `git stash` if in doubt, don't chase it here).

- [ ] **Step 7: Commit**

```bash
git add internal/handlers/xprocess_reconciler.go internal/handlers/xprocess_reconciler_test.go cmd/whatomate/main.go
git commit -m "feat(sales): daily XProcess reconciliation loop (XProcessReconciler)"
```

---

## Task 12: Bot — variável segura da integração no nó `api_call`

**Files:**
- Create: `internal/handlers/xprocess_integration_secrets.go`
- Create: `internal/handlers/xprocess_integration_secrets_test.go`
- Modify: `internal/handlers/chatbot_graph_runner.go` (`execChatAPICall`)

**Interfaces:**
- Produces: `(a *App) resolveIntegrationSecrets(orgID uuid.UUID, config map[string]any) (map[string]any, error)`.
- Consumes: `models.XProcessIntegration`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/handlers/xprocess_integration_secrets_test.go
package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveIntegrationSecrets_ReplacesPlaceholder(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	integ := models.XProcessIntegration{OrganizationID: org.ID, BaseURL: "https://api.atacadaodospisos.com.br", APIKey: "real-secret-key", IsActive: true}
	require.NoError(t, integ.EncryptSecrets(app.Config.App.EncryptionKey))
	require.NoError(t, app.DB.Create(&integ).Error)

	config := map[string]any{
		"url":     "https://api.atacadaodospisos.com.br/api/pedido",
		"headers": map[string]any{"x-api-key": "{{integrations.xprocess.api_key}}", "content-type": "application/json"},
	}

	resolved, err := app.ResolveIntegrationSecretsForTest(org.ID, config)
	require.NoError(t, err)
	headers := resolved["headers"].(map[string]any)
	assert.Equal(t, "real-secret-key", headers["x-api-key"])
	assert.Equal(t, "application/json", headers["content-type"], "other headers must pass through unchanged")

	// The original config passed in must not be mutated — resolving a
	// secret returns a copy.
	originalHeaders := config["headers"].(map[string]any)
	assert.Equal(t, "{{integrations.xprocess.api_key}}", originalHeaders["x-api-key"])
}

func TestResolveIntegrationSecrets_NoPlaceholder_PassesThroughUnchanged(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	config := map[string]any{"url": "https://example.com", "headers": map[string]any{"Authorization": "Bearer abc"}}

	resolved, err := app.ResolveIntegrationSecretsForTest(org.ID, config)
	require.NoError(t, err)
	headers := resolved["headers"].(map[string]any)
	assert.Equal(t, "Bearer abc", headers["Authorization"])
}

func TestResolveIntegrationSecrets_PlaceholderButNoIntegration_FailsClosed(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	config := map[string]any{"headers": map[string]any{"x-api-key": "{{integrations.xprocess.api_key}}"}}

	_, err := app.ResolveIntegrationSecretsForTest(org.ID, config)
	assert.Error(t, err, "must fail rather than send the literal placeholder string to a third party")
}

func TestResolveIntegrationSecrets_PlaceholderButIntegrationInactive_FailsClosed(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	integ := models.XProcessIntegration{OrganizationID: org.ID, BaseURL: "https://x", APIKey: "key", IsActive: false}
	require.NoError(t, integ.EncryptSecrets(app.Config.App.EncryptionKey))
	require.NoError(t, app.DB.Create(&integ).Error)

	config := map[string]any{"headers": map[string]any{"x-api-key": "{{integrations.xprocess.api_key}}"}}
	_, err := app.ResolveIntegrationSecretsForTest(org.ID, config)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/handlers/... -run TestResolveIntegrationSecrets -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/handlers/xprocess_integration_secrets.go
package handlers

import (
	"errors"
	"maps"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// xprocessAPIKeyPlaceholder is what a chatbot flow author types into a
// header value to use the organization's X2 credential — never the key
// itself. Resolved server-side only, right before the HTTP call is made;
// it is never written to SessionData and never passes through
// processTemplate, so it cannot leak into a later message or log line that
// happens to dump session variables (design §3: "Chave do X2 dentro de
// fluxos do bot").
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/handlers/... -run TestResolveIntegrationSecrets -v`
Expected: PASS, all 4 tests green.

- [ ] **Step 5: Wire into execChatAPICall**

In `internal/handlers/chatbot_graph_runner.go`, inside `execChatAPICall`, right before `cfgJSONB := models.JSONB(node.Config)`:

```go
func (a *App) execChatAPICall(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	resolvedConfig, err := a.resolveIntegrationSecrets(ctx.account.OrganizationID, node.Config)
	if err != nil {
		a.Log.Error("api_call node failed to resolve integration secret",
			"node", node.ID, "session", ctx.session.ID, "error", err)
		return nodeOutcome{outcome: "http:non2xx"}, nil
	}
	cfgJSONB := models.JSONB(resolvedConfig)
	// ... rest of the function is unchanged from here, but every reference
	// to node.Config below this point (e.g. the response_mapping and
	// message_template lookups later in the function) must keep reading
	// from node.Config, NOT resolvedConfig — only "headers" ever changes;
	// re-pointing those other lookups at resolvedConfig would still work
	// (they're untouched keys) but there is no reason to change them, so
	// leave them exactly as they are today.
```

Do not touch `execChatWebhook` or `fetchAPIContext` (`internal/handlers/chatbot_processor.go`) — the design's bot lookup flow (§7) only ever uses `api_call` nodes, and those two call sites are out of scope (avoid widening `executeConfiguredAPI`'s signature for a need that doesn't exist yet).

- [ ] **Step 6: Write an end-to-end test through execChatAPICall**

Drives it the same way `TestRunChatGraph_APICall_2xxRoutesAndMapsResponse` (`internal/handlers/chatbot_graph_runner_test.go`) already does — `newGraphTestFixtures(t)` builds `(app, org, account, contact, session)`, and `app.runChatGraph(account, contact, session, flow, "start", "", nil)` runs the graph end to end. Add to `internal/handlers/xprocess_integration_secrets_test.go`:

```go
func TestExecChatAPICall_UsesXProcessIntegration_EndToEnd(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"pedido":[]}`))
	}))
	defer srv.Close()

	app, org, account, contact, session := newGraphTestFixtures(t)
	integ := models.XProcessIntegration{OrganizationID: org.ID, BaseURL: "https://unused.example.com", APIKey: "real-secret-key", IsActive: true}
	require.NoError(t, integ.EncryptSecrets(app.Config.App.EncryptionKey))
	require.NoError(t, app.DB.Create(&integ).Error)

	flow := &models.ChatbotFlow{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  org.ID,
		WhatsAppAccount: account.Name,
		Name:            "xprocess-lookup",
		IsEnabled:       true,
		Graph: models.JSONB{
			"version":    2,
			"entry_node": "api",
			"nodes": []any{
				map[string]any{"id": "api", "type": "api_call", "label": "consultar pedido", "config": map[string]any{
					"url":     srv.URL,
					"method":  "POST",
					"headers": map[string]any{"x-api-key": "{{integrations.xprocess.api_key}}"},
					"body":    `{"numero_pedido":"666","documento":"12345678900"}`,
				}},
				map[string]any{"id": "end", "type": "end"},
			},
			"edges": []any{
				map[string]any{"from": "api", "to": "end", "condition": "http:2xx"},
			},
		},
	}
	require.NoError(t, app.DB.Create(flow).Error)

	require.NoError(t, app.runChatGraph(account, contact, session, flow, "start", "", nil))
	require.NoError(t, app.DB.First(session, session.ID).Error)
	assert.Equal(t, models.SessionStatusCompleted, session.Status)
	assert.Equal(t, "real-secret-key", gotAuth, "the flow's api_call node must send the real, decrypted X2 key — never the literal placeholder")
}

func TestExecChatAPICall_XProcessPlaceholderButNoIntegration_RoutesNon2xx(t *testing.T) {
	app, org, account, contact, session := newGraphTestFixtures(t)
	_ = org // no XProcessIntegration created for this org on purpose

	flow := &models.ChatbotFlow{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  org.ID,
		WhatsAppAccount: account.Name,
		Name:            "xprocess-lookup-unconfigured",
		IsEnabled:       true,
		Graph: models.JSONB{
			"version":    2,
			"entry_node": "api",
			"nodes": []any{
				map[string]any{"id": "api", "type": "api_call", "label": "consultar pedido", "config": map[string]any{
					"url":     "https://unused.example.com/api/pedido",
					"method":  "POST",
					"headers": map[string]any{"x-api-key": "{{integrations.xprocess.api_key}}"},
				}},
				map[string]any{"id": "end", "type": "end"},
				map[string]any{"id": "fallback", "type": "end"},
			},
			"edges": []any{
				map[string]any{"from": "api", "to": "end", "condition": "http:2xx"},
				map[string]any{"from": "api", "to": "fallback", "condition": "http:non2xx"},
			},
		},
	}
	require.NoError(t, app.DB.Create(flow).Error)

	require.NoError(t, app.runChatGraph(account, contact, session, flow, "start", "", nil))
	require.NoError(t, app.DB.First(session, session.ID).Error)
	assert.Equal(t, models.SessionStatusCompleted, session.Status)

	path := chatGraphPath(t, session)
	require.GreaterOrEqual(t, len(path), 2)
	assert.Equal(t, "http:non2xx", path[0]["outcome"], "an unconfigured integration must fail closed via http:non2xx, never send the literal placeholder")
}
```

Add `"github.com/google/uuid"` to this file's imports (needed for `uuid.New()` in the flow's `BaseModel.ID`).

- [ ] **Step 7: Run the full chatbot + xprocess test set**

Run: `go build ./... && go test ./internal/handlers/... -run 'TestResolveIntegrationSecrets|TestExecChatAPICall|TestRunChatGraph' -v`
Expected: PASS, no regressions in existing `api_call`/chat graph tests.

- [ ] **Step 8: Commit**

```bash
git add internal/handlers/xprocess_integration_secrets.go internal/handlers/xprocess_integration_secrets_test.go internal/handlers/chatbot_graph_runner.go
git commit -m "feat(chatbot): resolve X2 credential in api_call nodes via a safe placeholder"
```

---

## Task 13: Documentação do fluxo do bot (webhooks/features doc)

**Files:**
- Modify: `docs/src/content/docs/features/chatbot.mdx` (ou o arquivo equivalente que já documenta nós do construtor de fluxo — localizar via `grep -rln "api_call" docs/src/content/docs`)

**Interfaces:** nenhuma nova — só documentação de como montar o fluxo com o placeholder.

- [ ] **Step 1: Locate the existing api_call documentation**

Run: `grep -rln "api_call" docs/src/content/docs`

- [ ] **Step 2: Add a subsection documenting the X2 lookup pattern**

Add a short subsection (mirroring the existing doc's tone/format) explaining:
- The `{{integrations.xprocess.api_key}}` placeholder is available in any `api_call` header once Configurações → Integrações → ERP X2 has an active credential.
- Example flow for "por documento": `GET /api/clientes?documento={{documento}}` → `response_mapping: {"cod_cliente": "cliente.cod_cliente"}` → `GET /api/vendas?cod_cliente={{cod_cliente}}`.
- Example flow for "por número do pedido": `POST /api/pedido` with body `{"numero_pedido": "{{numero_pedido}}", "documento": "{{documento}}"}`.
- The data is always D-1 — recommend the flow's message template say "dados até o fechamento de ontem à noite".
- The bot must never echo the customer's full documento back into the conversation — only the last digits.

- [ ] **Step 3: Commit**

```bash
git add docs/src/content/docs/features/chatbot.mdx
git commit -m "docs(chatbot): document the X2 order-status lookup pattern for api_call nodes"
```

---

## Final Verification

- [ ] **Full backend suite**

Run:
```bash
export TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/whatomate_test?sslmode=disable'
export TEST_REDIS_URL='redis://localhost:6379/0'
go build ./... && go vet ./... && go test ./... -count=1 -p 1
```
Expected: PASS across every package, modulo the 3 pre-existing failures already tracked in project memory (2 Windows-symlink tests, 1 SLA-timestamp-truncation flake) — confirm any new failure isn't one of those before treating it as a regression.

- [ ] **Full frontend suite**

Run:
```bash
cd frontend
npm run typecheck
npx vitest run
npm run build
npx eslint src/views/settings/XProcessIntegrationView.vue src/views/settings/IntegrationsSettingsHubView.vue src/components/sales/XProcessLinkForm.vue src/services/api.ts --ext .vue,.ts
```
Expected: all clean.

- [ ] **Manual smoke test** (per design §9)

1. Configurar credencial em Integrações → ERP X2 com uma chave de teste real, clicar "Testar conexão", salvar.
2. Abrir uma oportunidade `aberta`, registrar um número de pedido + documento reais (existentes no X2), confirmar a mensagem "roda amanhã" e nenhuma chamada de rede na aba Network do devtools.
3. Rodar `RunXProcessReconciliation` manualmente (via um comando de debug, ou esperar o horário — documentar no PR qual dos dois foi usado) e confirmar que a oportunidade converte.
4. Montar um fluxo de bot mínimo com um nó `api_call` usando o placeholder, testar no simulador de conversa.

---

## Self-Review Notes (already applied above, kept here for the reviewer)

- **Spec coverage:** §4 (modelo de dados) → Tasks 2, 7; §5 (job) → Tasks 10, 11; §6 (agente registra) → Tasks 8, 9; §7 (bot) → Tasks 12, 13; §8 (permissões/pendências) → Tasks 3, 8; credencial/aba → Tasks 2–5. Every numbered section of the design has at least one task.
- **The 7-day-grace bug the design review caught** is covered by two dedicated regression tests in Task 10 (`Fechado_FirstTime_...StaysUnresolved` and `Cancelado_AlreadyConverted_...` — the latter specifically exercises CANCELADO arriving *inside* the grace window, which is exactly the scenario that would break if `resolved_at` were ever set eagerly).
- **Type consistency checked:** `xprocess.PedidoItem`/`PedidoResumo` (Task 1) are the only types Tasks 10–12 consume from that package; `models.SalesOpportunityXProcessLink` field names (Task 7) match every reference in Tasks 8, 10, 11 exactly (`FirstClosedAt`, `ConsecutiveNotFound`, `ResolvedAt`, `StatusXProcess`, `ValorVendido`, `Itens`).
- **Placeholder scan:** the one intentionally-left "find the real helper name" instructions (Task 3 Step 2's note, Task 12 Step 6) are not vague TODOs — they're explicit instructions to grep a named file and use whatever is already there, because this plan cannot see that file's exact current helper names without executing the earlier tasks first; leaving them unresolved here would be guessing, which is worse than pointing at the source of truth.
