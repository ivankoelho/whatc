# Tela de login com painel de marca configurável — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Redesenhar a tela de login em duas colunas (painel de marca à esquerda, formulário à direita) e permitir que um admin troque o painel esquerdo por uma imagem enviada via upload, guardada como configuração única do sistema (não por organização).

**Architecture:** Uma tabela nova `branding_settings` — linha única, semeada com ID fixo em tempo de migração (sem get-or-create em request nenhum) — mais quatro endpoints (dois públicos de leitura, dois autenticados de escrita) que reaproveitam padrões já existentes no backend: escrita atômica via temp-file+rename (`internal/tts/piper.go`), lock de linha via `clause.Locking{Strength:"UPDATE"}` (`internal/handlers/agent_transfers.go`), sniffing real de conteúdo via `http.DetectContentType` (stdlib), e o mesmo mecanismo de storage local já usado por mídia de conversa e de campanha (`getMediaStoragePath`/`ensureMediaDir`/`getExtensionFromMimeType` em `internal/handlers/media.go`). No frontend, `LoginView.vue` ganha duas colunas e busca a config publicamente; `SettingsView.vue` ganha um card de upload na aba Geral, mesmo padrão de UI já usado pelo upload de música de espera.

**Tech Stack:** Go 1.25, GORM + Postgres, fasthttp/fastglue — backend. Vue 3 + TypeScript, axios, vue-i18n — frontend.

## Global Constraints

- `BrandingSettings` é uma tabela de sistema, sem `organization_id` — não existe seletor de organização na tela de login, então não há como resolver "de qual org" antes de saber quem é o usuário.
- O ID da linha única é fixo: `00000000-0000-0000-0000-000000000001` (`models.BrandingSettingsSingletonID`). Nenhum handler faz get-or-create — a linha é semeada uma vez em `-migrate` via `EnsureBrandingSettingsRow`.
- Toda validação de tipo de arquivo é por conteúdo (`http.DetectContentType`), nunca pelo header `Content-Type` do multipart. Só `{image/jpeg, image/png, image/webp}` são aceitos — mesmo subconjunto de imagens que `internal/handlers/campaigns.go:887-889` já aceita.
- Escrita de arquivo é sempre temp-file + `os.Rename` (atômica no mesmo diretório). O arquivo anterior só é removido depois que a transação do banco confirma (`COMMIT`) o novo caminho — nunca antes.
- Toda a sequência (ler caminho antigo → escrever novo arquivo → apontar banco pro novo → só então apagar o antigo) roda sob um único lock de linha (`tx.Clauses(clause.Locking{Strength: "UPDATE"})`, tomado ANTES da escrita do arquivo) — sem índice único novo, sem mutex de processo.
- `GET /api/branding` nunca expõe mais que `{"login_background_url": null | string}` — nenhum outro campo da tabela.
- Todo handler autenticado usa `a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)` (`internal/handlers/app.go:262`), nunca checagem própria.
- Limite de upload: 5MB (`io.LimitReader`, mesmo padrão de `campaigns.go:871-880`).
- Chaves de i18n novas em `pt-BR.json`/`en.json`, na mesma posição relativa nos dois arquivos.

---

### Task 1: `BrandingSettings` — modelo, migração e seed do singleton

**Files:**
- Create: `internal/models/branding.go`
- Modify: `internal/database/postgres.go` (import de `gorm.io/gorm/clause`; `GetMigrationModels()`; nova função `EnsureBrandingSettingsRow`)
- Modify: `cmd/whatomate/main.go` (chama o seed no bloco `-migrate`)
- Modify: `test/testutil/db.go` (registra `branding_settings` na migração e limpeza de teste)
- Test: `internal/database/database_test.go`

**Interfaces:**
- Produces: `models.BrandingSettings{BaseModel, LoginBackgroundPath, LoginBackgroundContentType}`, `models.BrandingSettingsSingletonID` (uuid.UUID fixo), `database.EnsureBrandingSettingsRow(db *gorm.DB) error`.
- Consumes: nada de tarefas anteriores (primeira tarefa).

- [ ] **Step 1: Escrever o modelo**

```go
// internal/models/branding.go
package models

import "github.com/google/uuid"

// BrandingSettingsSingletonID is the fixed, well-known primary key of the
// one and only branding_settings row. There is no per-request get-or-create:
// the row is seeded once at migrate time (see database.EnsureBrandingSettingsRow),
// so every read/write always addresses this exact ID — never "find or create".
var BrandingSettingsSingletonID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// BrandingSettings is a system-wide (not per-organization) singleton — there
// is no organization selector on the login page, so this cannot be scoped to
// a tenant the way ChatbotSettings is. Both fields are empty when no custom
// background is configured, and the login page falls back to its default
// gradient.
type BrandingSettings struct {
	BaseModel
	LoginBackgroundPath        string `gorm:"size:500" json:"login_background_path,omitempty"`
	LoginBackgroundContentType string `gorm:"size:100" json:"login_background_content_type,omitempty"`
}

func (BrandingSettings) TableName() string { return "branding_settings" }
```

- [ ] **Step 2: Registrar para migração**

Em `internal/database/postgres.go`, adicione `"gorm.io/gorm/clause"` ao bloco de import (depois de `"gorm.io/gorm"`, antes de `"gorm.io/gorm/logger"`, ordem alfabética):

```go
import (
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)
```

Em `GetMigrationModels()`, no final da lista (depois de `{"AuditLog", &models.AuditLog{}}`):

```go
		{"AuditLog", &models.AuditLog{}},

		// Configuração de sistema (não por organização)
		{"BrandingSettings", &models.BrandingSettings{}},
	}
}
```

- [ ] **Step 3: Escrever a função de seed**

No mesmo arquivo `internal/database/postgres.go`, logo depois de `BackfillOccurrenceSourceForManualCases`:

```go
// EnsureBrandingSettingsRow seeds the one branding_settings row this system
// ever has, keyed by the fixed models.BrandingSettingsSingletonID. Runs once
// at migrate time; ON CONFLICT DO NOTHING makes re-running a safe no-op —
// the same idiom ensureDefaultSLAPolicies already uses for seeding under
// concurrency, here applied to the primary key directly instead of a partial
// unique index (there is exactly one row, ever, so the PK alone is enough).
func EnsureBrandingSettingsRow(db *gorm.DB) error {
	row := models.BrandingSettings{}
	row.ID = models.BrandingSettingsSingletonID
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}
```

- [ ] **Step 4: Chamar o seed no boot com `-migrate`**

Em `cmd/whatomate/main.go`, dentro do bloco `if *migrate {`, logo depois de `database.RunMigrationWithProgress(...)` e antes dos backfills de permissão existentes:

```go
	if *migrate {
		if err := database.RunMigrationWithProgress(db, &cfg.DefaultAdmin); err != nil {
			lo.Fatal("Migration failed", "error", err)
		}

		// Semeia a linha única de configuração de marca do sistema. Precisa
		// rodar aqui, depois do AutoMigrate (a tabela precisa existir) e antes
		// do ListenAndServe — os handlers de branding assumem que a linha já
		// existe, nunca fazem get-or-create.
		if err := database.EnsureBrandingSettingsRow(db); err != nil {
			lo.Fatal("Branding settings seed failed", "error", err)
		}

		// Backfill v2 graph for any legacy chatbot flow still on Steps[].
```

(O restante do bloco `-migrate` continua exatamente como está — só a chamada nova é inserida.)

- [ ] **Step 5: Registrar a tabela nova nas listas de migração/limpeza de teste**

Em `test/testutil/db.go`, dentro de `runMigrations`, adicione `&models.BrandingSettings{}` à lista de modelos migrados (mesmo padrão de `&models.Unit{}`/`&models.Department{}` já presentes ali). Dentro de `cleanupTables` E de `TruncateTables` (duas listas de nomes de tabela, ambas já têm `"departments"`/`"units"`), adicione `"branding_settings"` a ambas — sem essa entrada o teste de idempotência do seed (Step 6) ficaria com uma linha órfã de uma execução anterior que nenhuma truncagem limparia.

- [ ] **Step 6: Escrever os testes**

```go
// em internal/database/database_test.go, perto de TestBackfillOccurrenceSourceForManualCases

func TestEnsureBrandingSettingsRow_CreatesTheSingletonRow(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	require.NoError(t, database.EnsureBrandingSettingsRow(db))

	var row models.BrandingSettings
	require.NoError(t, db.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error)
	assert.Equal(t, models.BrandingSettingsSingletonID, row.ID)
	assert.Empty(t, row.LoginBackgroundPath)
}

func TestEnsureBrandingSettingsRow_IsIdempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	require.NoError(t, database.EnsureBrandingSettingsRow(db))
	// Simula um upload já ter acontecido antes de uma segunda chamada ao seed
	// (ex: reiniciar com -migrate depois de já estar configurado).
	require.NoError(t, db.Model(&models.BrandingSettings{}).
		Where("id = ?", models.BrandingSettingsSingletonID).
		Update("login_background_path", "branding/login-background.jpg").Error)

	require.NoError(t, database.EnsureBrandingSettingsRow(db))

	var count int64
	db.Model(&models.BrandingSettings{}).Count(&count)
	assert.EqualValues(t, 1, count, "must never create a second row")

	var row models.BrandingSettings
	require.NoError(t, db.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error)
	assert.Equal(t, "branding/login-background.jpg", row.LoginBackgroundPath,
		"re-running the seed must not overwrite an existing configuration")
}
```

Confirme que `internal/database/database_test.go` já importa `github.com/stretchr/testify/assert` — se não importar, adicione ao bloco de import.

- [ ] **Step 7: Rodar os testes**

Run: `TEST_DATABASE_URL="host=localhost user=postgres password=postgres dbname=whatomate_test port=5432 sslmode=disable" go test ./internal/database/... -run 'TestEnsureBrandingSettingsRow_' -v`
Expected: ambos PASS.

Run também: `go build ./... && go vet ./...` — deve ficar limpo (garante que o novo import `clause` e a nova chamada em `main.go` compilam).

- [ ] **Step 8: Commit**

```bash
git add internal/models/branding.go internal/database/postgres.go cmd/whatomate/main.go \
        test/testutil/db.go internal/database/database_test.go
git commit -m "$(cat <<'EOF'
feat(branding): adiciona branding_settings, singleton semeado em -migrate

Configuracao de sistema, nao por organizacao -- nao ha seletor de org
na tela de login. ID fixo (00000000-0000-0000-0000-000000000001),
semeado uma vez via ON CONFLICT DO NOTHING; nenhum handler faz
get-or-create em request.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Endpoints públicos de leitura — `GET /api/branding` e `GET /api/branding/login-background`

**Files:**
- Create: `internal/handlers/branding.go`
- Modify: `cmd/whatomate/main.go` (rotas)
- Test: `internal/handlers/branding_test.go`

**Interfaces:**
- Consumes: `models.BrandingSettings`, `models.BrandingSettingsSingletonID` (Task 1); `a.getMediaStoragePath()`, `getExtensionFromMimeType` (existentes, `internal/handlers/media.go`).
- Produces: `a.GetPublicBranding`, `a.ServeLoginBackground` — consumidos pelo Task 5 (frontend).

- [ ] **Step 1: Escrever os dois handlers públicos**

```go
// internal/handlers/branding.go
package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// GetPublicBranding returns the login page's background image URL, or null
// when none is configured. Public — the login page renders before anyone is
// authenticated, so this cannot require auth. The response is deliberately
// minimal: only login_background_url, never created_at/updated_at/id or any
// other internal field.
func (a *App) GetPublicBranding(r *fastglue.Request) error {
	var row models.BrandingSettings
	if err := a.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error; err != nil {
		a.Log.Error("Failed to load branding settings", "error", err)
		return r.SendEnvelope(map[string]any{"login_background_url": nil})
	}

	if row.LoginBackgroundPath == "" {
		return r.SendEnvelope(map[string]any{"login_background_url": nil})
	}

	url := fmt.Sprintf("/api/branding/login-background?v=%d", row.UpdatedAt.Unix())
	return r.SendEnvelope(map[string]any{"login_background_url": url})
}

// ServeLoginBackground serves the configured background image file. Public,
// same path-traversal/symlink protection as ServeMedia (media.go).
func (a *App) ServeLoginBackground(r *fastglue.Request) error {
	var row models.BrandingSettings
	if err := a.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error; err != nil || row.LoginBackgroundPath == "" {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "No background image configured", nil, "")
	}

	baseDir, err := filepath.Abs(a.getMediaStoragePath())
	if err != nil {
		a.Log.Error("Storage configuration error", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Storage configuration error", nil, "")
	}
	fullPath, err := filepath.Abs(filepath.Join(baseDir, filepath.Clean(row.LoginBackgroundPath)))
	if err != nil || !strings.HasPrefix(fullPath, baseDir+string(os.PathSeparator)) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid file path", nil, "")
	}

	info, err := os.Lstat(fullPath)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "File not found", nil, "")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid file path", nil, "")
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		a.Log.Error("Failed to read branding file", "path", fullPath, "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to read file", nil, "")
	}

	contentType := row.LoginBackgroundContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	r.RequestCtx.Response.Header.Set("Content-Type", contentType)
	r.RequestCtx.Response.Header.Set("Cache-Control", "public, max-age=3600")
	r.RequestCtx.SetBody(data)
	return nil
}
```

- [ ] **Step 2: Registrar as rotas**

Em `cmd/whatomate/main.go`, logo depois do bloco de rotas de SSO público (perto de `g.GET("/api/auth/sso/providers", app.GetPublicSSOProviders)`):

```go
	// Branding do sistema (config única, não por organização) — os dois GETs
	// são públicos porque a tela de login carrega antes de qualquer auth.
	g.GET("/api/branding", app.GetPublicBranding)
	g.GET("/api/branding/login-background", app.ServeLoginBackground)
```

- [ ] **Step 3: Escrever os testes**

```go
// internal/handlers/branding_test.go
package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestGetPublicBranding_NullWhenNotConfigured(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	req := testutil.NewGETRequest(t)
	require.NoError(t, app.GetPublicBranding(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, `"login_background_url":null`)
	assert.NotContains(t, body, "created_at", "response must never leak internal fields")
	assert.NotContains(t, body, "updated_at")
}

func TestGetPublicBranding_ReturnsURLWhenConfigured(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))
	require.NoError(t, app.DB.Model(&models.BrandingSettings{}).
		Where("id = ?", models.BrandingSettingsSingletonID).
		Updates(map[string]any{
			"login_background_path":         "branding/login-background.jpg",
			"login_background_content_type": "image/jpeg",
		}).Error)

	req := testutil.NewGETRequest(t)
	require.NoError(t, app.GetPublicBranding(req))
	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, "/api/branding/login-background?v=")
}

func TestGetPublicBranding_RequiresNoAuthentication(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	// Deliberately no SetAuthContext call — proves the endpoint is genuinely
	// public, not just unchecked.
	req := testutil.NewGETRequest(t)
	require.NoError(t, app.GetPublicBranding(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
}

func TestServeLoginBackground_404WhenNotConfigured(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	req := testutil.NewGETRequest(t)
	require.NoError(t, app.ServeLoginBackground(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
}

func TestServeLoginBackground_ServesConfiguredFile(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "branding"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "branding", "login-background.jpg"), []byte("fake-jpeg-bytes"), 0644))
	require.NoError(t, app.DB.Model(&models.BrandingSettings{}).
		Where("id = ?", models.BrandingSettingsSingletonID).
		Updates(map[string]any{
			"login_background_path":         "branding/login-background.jpg",
			"login_background_content_type": "image/jpeg",
		}).Error)

	req := testutil.NewGETRequest(t)
	require.NoError(t, app.ServeLoginBackground(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Equal(t, []byte("fake-jpeg-bytes"), testutil.GetResponseBody(req))
}

func TestServeLoginBackground_RejectsPathTraversal(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("should not be reachable"), 0644))

	require.NoError(t, app.DB.Model(&models.BrandingSettings{}).
		Where("id = ?", models.BrandingSettingsSingletonID).
		Update("login_background_path", "../"+filepath.Base(filepath.Dir(outside))+"/secret.txt").Error)

	req := testutil.NewGETRequest(t)
	require.NoError(t, app.ServeLoginBackground(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}
```

Adicione `"os"` e `"path/filepath"` ao bloco de import do arquivo de teste (necessários para `TestServeLoginBackground_ServesConfiguredFile`/`RejectsPathTraversal`).

- [ ] **Step 4: Rodar os testes**

Run: `TEST_DATABASE_URL="host=localhost user=postgres password=postgres dbname=whatomate_test port=5432 sslmode=disable" TEST_REDIS_URL="redis://localhost:6379/0" go test ./internal/handlers/... -run 'TestGetPublicBranding_|TestServeLoginBackground_' -v`
Expected: todos PASS.

Run: `go build ./... && go vet ./...` — limpo.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/branding.go cmd/whatomate/main.go internal/handlers/branding_test.go
git commit -m "$(cat <<'EOF'
feat(branding): endpoints publicos de leitura do background de login

GET /api/branding devolve so login_background_url (null ou string),
nunca metadados internos. GET /api/branding/login-background serve o
arquivo com a mesma protecao contra path traversal/symlink de
ServeMedia. Nenhum dos dois exige autenticacao -- a tela de login
carrega antes de qualquer sessao existir.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Upload — `POST /api/branding/login-background`

**Files:**
- Modify: `internal/handlers/branding.go`
- Modify: `cmd/whatomate/main.go` (rota)
- Test: `internal/handlers/branding_test.go`

**Interfaces:**
- Consumes: `models.BrandingSettings`, `models.BrandingSettingsSingletonID` (Task 1); `a.requireAuth` (existente); `a.ensureMediaDir`, `a.getMediaStoragePath`, `getExtensionFromMimeType` (existentes, `media.go`); `clause.Locking` (`gorm.io/gorm/clause`, já usado em `agent_transfers.go`).
- Produces: `a.UploadLoginBackground` — mesmo shape de resposta de `GetPublicBranding` (`{"login_background_url": ...}`).

- [ ] **Step 1: Escrever o handler de upload**

Adicione ao final de `internal/handlers/branding.go` (e estenda o bloco de import: `"io"`, `"net/http"`, `"time"`, `"github.com/shridarpatil/whatomate/internal/models"` já presente, `"gorm.io/gorm/clause"`):

```go
import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm/clause"
)
```

```go
// brandingAllowedContentTypes are the image types this endpoint accepts —
// the same image subset campaigns.go already allows for media uploads
// (internal/handlers/campaigns.go:887-889).
var brandingAllowedContentTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

// brandingMaxUploadSize matches the 5MB precedent already used for audio
// uploads in this codebase's settings (SettingsView.vue's hold music/
// ringback upload UI advertises the same limit).
const brandingMaxUploadSize = 5 << 20

// UploadLoginBackground replaces the login page's background image.
//
// Five things this deliberately gets right, in order:
//  1. Real content-type detection (http.DetectContentType on the actual
//     bytes) — never trusts the multipart Content-Type header.
//  2. Atomic write: writes to a temp file, only os.Rename's it into place
//     once the write is complete and validated.
//  3. Extension changes safely: the new file's name already reflects its
//     real extension; the OLD file (possibly a different extension) is
//     deleted only after the database transaction below commits.
//  4. Concurrency: the row lock is taken BEFORE any file I/O, and held
//     across the whole read-old-path/write-new-file/update-pointer
//     sequence — not just the final UPDATE. A second concurrent upload
//     blocks until the first fully finishes and sees fresh state.
//  5. Size-bounded before any disk write (5MB).
func (a *App) UploadLoginBackground(r *fastglue.Request) error {
	_, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}

	fileHeader, err := r.RequestCtx.FormFile("file")
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "No file provided", nil, "")
	}

	file, err := fileHeader.Open()
	if err != nil {
		a.Log.Error("Failed to open uploaded file", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to open uploaded file", nil, "")
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, brandingMaxUploadSize+1))
	if err != nil {
		a.Log.Error("Failed to read uploaded file", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to read file", nil, "")
	}
	if len(data) > brandingMaxUploadSize {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "File too large. Maximum size is 5MB", nil, "")
	}

	// Real content-type detection — the point #3 the design review insisted
	// on: never trust what the client's multipart header claims.
	sniffed := http.DetectContentType(data)
	if !brandingAllowedContentTypes[sniffed] {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Unsupported file type: "+sniffed, nil, "")
	}

	if err := a.ensureMediaDir("branding"); err != nil {
		a.Log.Error("Failed to create branding directory", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Storage configuration error", nil, "")
	}

	ext := getExtensionFromMimeType(sniffed)
	newRelPath := filepath.Join("branding", "login-background"+ext)
	basePath := a.getMediaStoragePath()
	finalPath := filepath.Join(basePath, newRelPath)
	tmpPath := finalPath + ".tmp"

	// Lock BEFORE any file I/O — a second concurrent upload must wait here,
	// not race the file write below. Mirrors agent_transfers.go's FOR UPDATE
	// pick pattern, without SKIP LOCKED (this is a singleton, not a queue:
	// the second request must wait and see fresh state, never skip).
	tx := a.DB.Begin()
	defer func() {
		if rec := recover(); rec != nil {
			tx.Rollback()
		}
	}()

	var row models.BrandingSettings
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error; err != nil {
		tx.Rollback()
		a.Log.Error("Failed to lock branding settings row", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save branding settings", nil, "")
	}
	oldRelPath := row.LoginBackgroundPath

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		tx.Rollback()
		a.Log.Error("Failed to write temp branding file", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save file", nil, "")
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		tx.Rollback()
		_ = os.Remove(tmpPath)
		a.Log.Error("Failed to finalize branding file", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save file", nil, "")
	}

	if err := tx.Model(&row).Updates(map[string]any{
		"login_background_path":         newRelPath,
		"login_background_content_type": sniffed,
	}).Error; err != nil {
		tx.Rollback()
		a.Log.Error("Failed to update branding settings", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save branding settings", nil, "")
	}

	if err := tx.Commit().Error; err != nil {
		a.Log.Error("Failed to commit branding settings update", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save branding settings", nil, "")
	}

	// Only after commit: the database now points at the new file, so the
	// previous one (possibly a different extension) is safe to remove.
	// Best-effort — the database is already the source of truth even if
	// this fails; a log line is enough, the request already succeeded.
	if oldRelPath != "" && oldRelPath != newRelPath {
		if err := os.Remove(filepath.Join(basePath, oldRelPath)); err != nil && !os.IsNotExist(err) {
			a.Log.Error("Failed to remove previous branding file", "path", oldRelPath, "error", err)
		}
	}

	return r.SendEnvelope(map[string]any{
		"login_background_url": fmt.Sprintf("/api/branding/login-background?v=%d", time.Now().Unix()),
	})
}
```

(`ServeLoginBackground`/`GetPublicBranding` do Task 2 continuam no mesmo arquivo, sem mudança — só o bloco de import no topo do arquivo precisa incorporar os símbolos novos listados acima.)

- [ ] **Step 2: Registrar a rota**

Em `cmd/whatomate/main.go`, logo depois da linha de `ServeLoginBackground` do Task 2:

```go
	g.GET("/api/branding", app.GetPublicBranding)
	g.GET("/api/branding/login-background", app.ServeLoginBackground)
	g.POST("/api/branding/login-background", app.UploadLoginBackground)
```

- [ ] **Step 3: Escrever os testes**

Acrescente a `internal/handlers/branding_test.go` (mesmo arquivo do Task 2; precisa de um helper de multipart — verifique primeiro se já existe um em `test/testutil` ou em algum `_test.go` de upload deste pacote, ex: `internal/handlers/campaigns_test.go` ou `templates_test.go`, procurando por `multipart.NewWriter`; reaproveite o helper existente se houver um, em vez de escrever um novo):

```go
func TestUploadLoginBackground_RejectedForUserWithoutPermission(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	req := newMultipartUploadRequest(t, "file", "bg.jpg", jpegMagicBytes())
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestUploadLoginBackground_AcceptsRealImageDespiteMismatchedHeader(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := newMultipartUploadRequest(t, "file", "bg.jpg", jpegMagicBytes())
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var row models.BrandingSettings
	require.NoError(t, app.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error)
	assert.Equal(t, "image/jpeg", row.LoginBackgroundContentType)
	assert.Equal(t, filepath.Join("branding", "login-background.jpg"), row.LoginBackgroundPath)
	assert.FileExists(t, filepath.Join(dir, "branding", "login-background.jpg"))
}

func TestUploadLoginBackground_RejectsSpoofedContentType(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	// The multipart part below declares Content-Type: image/jpeg, but the
	// bytes are plain text — proves validation is by sniffed content, not
	// by the header the client sent.
	req := newMultipartUploadRequestWithContentType(t, "file", "bg.jpg", []byte("this is not an image"), "image/jpeg")
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))

	var row models.BrandingSettings
	require.NoError(t, app.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error)
	assert.Empty(t, row.LoginBackgroundPath, "a rejected upload must not touch the stored config")
}

func TestUploadLoginBackground_RejectsOversizedFile(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	oversized := append(jpegMagicBytes(), make([]byte, 6<<20)...) // >5MB
	req := newMultipartUploadRequest(t, "file", "bg.jpg", oversized)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))

	entries, err := os.ReadDir(filepath.Join(dir, "branding"))
	if err == nil {
		for _, e := range entries {
			assert.NotContains(t, e.Name(), ".tmp", "an oversized upload must not leave a temp file behind")
		}
	}
}

func TestUploadLoginBackground_ExtensionChangeLeavesNoOrphan(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	first := newMultipartUploadRequest(t, "file", "bg.jpg", jpegMagicBytes())
	testutil.SetAuthContext(first, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(first))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(first))
	assert.FileExists(t, filepath.Join(dir, "branding", "login-background.jpg"))

	second := newMultipartUploadRequest(t, "file", "bg.png", pngMagicBytes())
	testutil.SetAuthContext(second, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(second))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(second))

	assert.NoFileExists(t, filepath.Join(dir, "branding", "login-background.jpg"),
		"the old extension's file must be gone after a successful switch")
	assert.FileExists(t, filepath.Join(dir, "branding", "login-background.png"))

	var row models.BrandingSettings
	require.NoError(t, app.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error)
	assert.Equal(t, filepath.Join("branding", "login-background.png"), row.LoginBackgroundPath)
}

// jpegMagicBytes/pngMagicBytes return the minimum byte sequence
// http.DetectContentType needs to identify each format for real —
// not a full valid image, just enough of the real file signature.
func jpegMagicBytes() []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 32)...)
}

func pngMagicBytes() []byte {
	return append([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, 32)...)
}
```

Confirmado: não existe nenhum helper de request multipart em `internal/handlers/*_test.go` hoje (`grep -rln "multipart.NewWriter" internal/handlers/*.go` não retorna nada) — o helper abaixo é novo, não uma duplicação. Sua construção de `*fastglue.Request` segue exatamente o mesmo shape de `testutil.NewGETRequest`/`NewJSONRequest` (`test/testutil/http.go:14-38`: aloca um `*fasthttp.RequestCtx`, configura método/headers/body, embrulha em `&fastglue.Request{RequestCtx: ctx}`) — compatível de verdade com `SetAuthContext`/`GetResponseStatusCode`/`GetResponseBody`, não é uma suposição. Adicione a `branding_test.go`:

```go
func newMultipartUploadRequest(t *testing.T, fieldName, filename string, content []byte) *fastglue.Request {
	t.Helper()
	return newMultipartUploadRequestWithContentType(t, fieldName, filename, content, "application/octet-stream")
}

func newMultipartUploadRequestWithContentType(t *testing.T, fieldName, filename string, content []byte, contentType string) *fastglue.Request {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, filename))
	partHeader.Set("Content-Type", contentType)
	part, err := writer.CreatePart(partHeader)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.Header.SetContentType(writer.FormDataContentType())
	ctx.Request.SetBody(buf.Bytes())

	return &fastglue.Request{RequestCtx: ctx}
}
```

Ajuste os imports de `branding_test.go` conforme o que for efetivamente usado (`bytes`, `mime/multipart`, `net/textproto`, `os`, `path/filepath`, `fmt`, além dos já usados no Task 2).

- [ ] **Step 4: Rodar os testes**

Run: `TEST_DATABASE_URL="host=localhost user=postgres password=postgres dbname=whatomate_test port=5432 sslmode=disable" TEST_REDIS_URL="redis://localhost:6379/0" go test ./internal/handlers/... -run 'TestUploadLoginBackground_' -v`
Expected: todos PASS.

Run: `go build ./... && go vet ./...` — limpo.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/branding.go cmd/whatomate/main.go internal/handlers/branding_test.go
git commit -m "$(cat <<'EOF'
feat(branding): upload atomico do background de login

Validacao por sniffing real (http.DetectContentType), nunca pelo
header multipart. Escrita atomica via temp-file+rename. Lock de linha
tomado antes de qualquer I/O de arquivo, cobrindo a sequencia inteira
-- nao so o UPDATE final. Arquivo de extensao anterior so e removido
depois do commit confirmar o novo caminho.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `DELETE /api/branding/login-background`

**Files:**
- Modify: `internal/handlers/branding.go`
- Modify: `cmd/whatomate/main.go` (rota)
- Test: `internal/handlers/branding_test.go`

**Interfaces:**
- Consumes: mesmos símbolos do Task 3 (`models.BrandingSettings`, `clause.Locking`, `a.requireAuth`, `a.getMediaStoragePath`).
- Produces: `a.DeleteLoginBackground` — resposta `{"deleted": true}`.

- [ ] **Step 1: Escrever o handler**

Adicione ao final de `internal/handlers/branding.go`:

```go
// DeleteLoginBackground clears the configured background image, reverting
// the login page to its default gradient. Same lock-before-file-I/O shape as
// UploadLoginBackground — a delete racing an upload (or another delete) must
// never leave the database and disk pointing at inconsistent state.
func (a *App) DeleteLoginBackground(r *fastglue.Request) error {
	_, _, err := a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if err != nil {
		return nil
	}

	basePath := a.getMediaStoragePath()

	tx := a.DB.Begin()
	defer func() {
		if rec := recover(); rec != nil {
			tx.Rollback()
		}
	}()

	var row models.BrandingSettings
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error; err != nil {
		tx.Rollback()
		a.Log.Error("Failed to lock branding settings row", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update branding settings", nil, "")
	}
	oldRelPath := row.LoginBackgroundPath

	if err := tx.Model(&row).Updates(map[string]any{
		"login_background_path":         "",
		"login_background_content_type": "",
	}).Error; err != nil {
		tx.Rollback()
		a.Log.Error("Failed to clear branding settings", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update branding settings", nil, "")
	}

	if err := tx.Commit().Error; err != nil {
		a.Log.Error("Failed to commit branding settings clear", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update branding settings", nil, "")
	}

	if oldRelPath != "" {
		if err := os.Remove(filepath.Join(basePath, oldRelPath)); err != nil && !os.IsNotExist(err) {
			a.Log.Error("Failed to remove branding file", "path", oldRelPath, "error", err)
		}
	}

	return r.SendEnvelope(map[string]any{"deleted": true})
}
```

- [ ] **Step 2: Registrar a rota**

Em `cmd/whatomate/main.go`, logo depois da linha do `POST` do Task 3:

```go
	g.GET("/api/branding", app.GetPublicBranding)
	g.GET("/api/branding/login-background", app.ServeLoginBackground)
	g.POST("/api/branding/login-background", app.UploadLoginBackground)
	g.DELETE("/api/branding/login-background", app.DeleteLoginBackground)
```

- [ ] **Step 3: Escrever os testes**

```go
func TestDeleteLoginBackground_RejectedForUserWithoutPermission(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	req := testutil.NewGETRequest(t) // método real vem do routing; o handler só olha auth+DB
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.DeleteLoginBackground(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestDeleteLoginBackground_ClearsConfigAndRemovesFile(t *testing.T) {
	app := newTestApp(t)
	dir := t.TempDir()
	app.Config.Storage.LocalPath = dir
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	uploadReq := newMultipartUploadRequest(t, "file", "bg.jpg", jpegMagicBytes())
	testutil.SetAuthContext(uploadReq, org.ID, user.ID)
	require.NoError(t, app.UploadLoginBackground(uploadReq))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(uploadReq))
	require.FileExists(t, filepath.Join(dir, "branding", "login-background.jpg"))

	deleteReq := testutil.NewGETRequest(t)
	testutil.SetAuthContext(deleteReq, org.ID, user.ID)
	require.NoError(t, app.DeleteLoginBackground(deleteReq))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(deleteReq))

	assert.NoFileExists(t, filepath.Join(dir, "branding", "login-background.jpg"))

	var row models.BrandingSettings
	require.NoError(t, app.DB.Where("id = ?", models.BrandingSettingsSingletonID).First(&row).Error)
	assert.Empty(t, row.LoginBackgroundPath)
	assert.Empty(t, row.LoginBackgroundContentType)

	getReq := testutil.NewGETRequest(t)
	require.NoError(t, app.GetPublicBranding(getReq))
	assert.Contains(t, string(testutil.GetResponseBody(getReq)), `"login_background_url":null`)
}

func TestDeleteLoginBackground_NoOpWhenAlreadyEmpty(t *testing.T) {
	app := newTestApp(t)
	require.NoError(t, database.EnsureBrandingSettingsRow(app.DB))

	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.DeleteLoginBackground(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), "deleting an already-empty config must succeed, not error")
}
```

- [ ] **Step 4: Rodar os testes**

Run: `TEST_DATABASE_URL="host=localhost user=postgres password=postgres dbname=whatomate_test port=5432 sslmode=disable" TEST_REDIS_URL="redis://localhost:6379/0" go test ./internal/handlers/... -run 'TestDeleteLoginBackground_' -v`
Expected: todos PASS.

Rode também a suíte completa de branding para confirmar que nada regrediu: `... go test ./internal/handlers/... -run 'TestGetPublicBranding_|TestServeLoginBackground_|TestUploadLoginBackground_|TestDeleteLoginBackground_' -v`

Run: `go build ./... && go vet ./...` — limpo.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/branding.go cmd/whatomate/main.go internal/handlers/branding_test.go
git commit -m "$(cat <<'EOF'
feat(branding): endpoint de remocao do background de login

Mesmo padrao de lock-antes-do-arquivo do upload. Remover config ja
vazia e sucesso (200), nao erro -- idempotente do ponto de vista do
cliente.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Frontend — `LoginView.vue` em duas colunas

**Files:**
- Modify: `frontend/src/views/auth/LoginView.vue`

**Interfaces:**
- Consumes: `GET /api/branding` (Task 2) — chamado sem autenticação, mesmo axios `api` já importado no arquivo.
- Produces: nada consumido por tarefas seguintes (a UI de upload, Task 6, não depende da UI de exibição).

- [ ] **Step 1: Adicionar o estado e a busca da config de branding**

Em `frontend/src/views/auth/LoginView.vue`, no `<script setup>`, logo depois da declaração de `ssoProviders`:

```ts
const loginBackgroundUrl = ref<string | null>(null)
```

Dentro do `onMounted` existente, em paralelo com a busca de SSO providers (troque o bloco `try/catch` de SSO por um `Promise.all`, mantendo exatamente o mesmo comportamento de erro silencioso para cada chamada):

```ts
onMounted(async () => {
  // Check for SSO error in query params
  const ssoError = route.query.sso_error as string
  if (ssoError) {
    toast.error(decodeURIComponent(ssoError))
    // Clear the error from URL
    router.replace({ query: { ...route.query, sso_error: undefined } })
  }

  await Promise.all([
    (async () => {
      try {
        const response = await api.get('/auth/sso/providers')
        ssoProviders.value = response.data.data || []
      } catch {
        ssoProviders.value = []
      }
    })(),
    (async () => {
      try {
        const response = await api.get('/branding')
        const url = response.data?.data?.login_background_url ?? response.data?.login_background_url
        loginBackgroundUrl.value = url ?? null
      } catch {
        // A tela de login nunca pode travar por causa dessa config opcional
        // -- qualquer falha aqui (rede, 500, JSON malformado) vira "sem imagem".
        loginBackgroundUrl.value = null
      }
    })()
  ])
})
```

- [ ] **Step 2: Reestruturar o template em duas colunas**

Troque o `<template>` inteiro por:

```html
<template>
  <div class="min-h-screen flex bg-[#0a0a0b] light:bg-gray-50">
    <!-- Painel de marca -->
    <div
      class="hidden lg:flex lg:w-1/2 relative flex-col justify-between p-12 bg-gradient-to-br from-emerald-600 to-green-800"
      :style="loginBackgroundUrl ? { backgroundImage: `url(${loginBackgroundUrl})`, backgroundSize: 'cover', backgroundPosition: 'center' } : {}"
    >
      <div v-if="loginBackgroundUrl" class="absolute inset-0 bg-black/40" />
      <div class="relative flex items-center gap-3">
        <div class="h-10 w-10 rounded-xl bg-white/10 backdrop-blur flex items-center justify-center">
          <MessageSquare class="h-6 w-6 text-white" />
        </div>
        <span class="text-white font-semibold text-lg">Whatomate</span>
      </div>
      <div class="relative">
        <p class="text-3xl font-bold text-white leading-tight">
          Todo o seu atendimento<br />
          Centralizado e em tempo real<br />
          num só lugar.
        </p>
      </div>
    </div>

    <!-- Painel de formulário -->
    <div class="flex-1 flex items-center justify-center p-4">
      <div class="w-full max-w-md rounded-2xl border border-white/[0.08] bg-white/[0.02] backdrop-blur light:bg-white light:border-gray-200 light:shadow-xl">
        <div class="p-8 space-y-1 text-center">
          <div class="flex justify-center mb-4 lg:hidden">
            <div class="h-12 w-12 rounded-xl bg-gradient-to-br from-emerald-500 to-green-600 flex items-center justify-center shadow-lg shadow-emerald-500/20">
              <MessageSquare class="h-7 w-7 text-white" />
            </div>
          </div>
          <h2 class="text-2xl font-bold text-white light:text-gray-900">{{ $t('auth.welcomeTitle') }}</h2>
          <p class="text-white/50 light:text-gray-500">
            {{ $t('auth.welcomeSubtitle') }}
          </p>
        </div>

        <form @submit.prevent="handleLogin">
          <div class="px-8 pb-4 space-y-4">
            <div class="space-y-2">
              <Label for="email" class="text-white/70 light:text-gray-700">{{ $t('common.email') }}</Label>
              <Input
                id="email"
                v-model="email"
                type="email"
                :placeholder="$t('auth.emailPlaceholder')"
                :disabled="isLoading"
                autocomplete="email"
              />
            </div>
            <div class="space-y-2">
              <Label for="password" class="text-white/70 light:text-gray-700">{{ $t('auth.password') }}</Label>
              <Input
                id="password"
                v-model="password"
                type="password"
                :placeholder="$t('auth.passwordPlaceholder')"
                :disabled="isLoading"
                autocomplete="current-password"
              />
            </div>
            <Button type="submit" class="w-full bg-gradient-to-r from-emerald-500 to-green-600 hover:from-emerald-600 hover:to-green-700 text-white shadow-lg shadow-emerald-500/20" :disabled="isLoading">
              <Loader2 v-if="isLoading" class="mr-2 h-4 w-4 animate-spin" />
              {{ $t('auth.signIn') }}
            </Button>
          </div>
        </form>

        <!-- SSO Section -->
        <div v-if="ssoProviders.length > 0" class="px-8 pb-4 space-y-3">
          <div class="relative my-2">
            <Separator class="bg-white/[0.08] light:bg-gray-200" />
            <span class="absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 bg-[#0a0a0b] light:bg-white px-2 text-xs text-white/40 light:text-gray-500">
              {{ $t('auth.orContinueWith') }}
            </span>
          </div>

          <Button
            v-for="provider in ssoProviders"
            :key="provider.provider"
            variant="outline"
            class="w-full justify-start gap-3 transition-colors bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50"
            :class="providerColors[provider.provider] || providerColors.custom"
            @click="initiateSSO(provider.provider)"
          >
            <svg class="h-5 w-5" viewBox="0 0 24 24" fill="currentColor">
              <path :d="providerIcons[provider.provider] || providerIcons.custom" />
            </svg>
            {{ provider.name }}
          </Button>
        </div>

        <div class="px-8 pb-8">
          <p class="text-sm text-center text-white/40 light:text-gray-500">
            {{ $t('auth.noAccount') }}
            <RouterLink to="/register" class="text-emerald-400 light:text-emerald-600 hover:underline">
              {{ $t('auth.signUp') }}
            </RouterLink>
          </p>
        </div>
      </div>
    </div>
  </div>
</template>
```

Note o que mudou de verdade em relação ao arquivo atual: o `<div>` raiz deixou de centralizar um card único e virou `flex` de duas colunas; o painel esquerdo é novo; o card de login em si (título, form, SSO, link de registro) é byte-a-byte o conteúdo que já existia, só que agora dentro do painel direito, com o logo pequeno do topo do card marcado `lg:hidden` (em telas largas o logo já aparece no painel esquerdo, não precisa duplicar).

- [ ] **Step 2: Verificar manualmente**

Suba o backend (`make run-migrate`) e o frontend (`make frontend-dev` ou `cd frontend && npm run dev`). Abra `http://localhost:3000/login` (ou a porta configurada) em uma tela larga (≥1024px):
- Sem nenhuma imagem configurada: painel esquerdo mostra gradiente verde, logo "Whatomate" e a frase de efeito.
- Painel direito continua funcional: digitar e-mail/senha e logar funciona exatamente como antes.
- Estreite a janela abaixo de 1024px: painel esquerdo desaparece, só o card de login aparece centralizado — igual ao comportamento atual.

Depois do Task 6 (upload funcionando), repita com uma imagem configurada: o painel esquerdo deve mostrar a imagem com overlay escuro, texto ainda legível.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/views/auth/LoginView.vue
git commit -m "$(cat <<'EOF'
feat(auth): tela de login em duas colunas com painel de marca

Painel esquerdo (gradiente verde por padrao, ou a imagem configurada
via GET /api/branding publico) + painel direito com o card de login
existente, inalterado. Falha ao buscar a config de branding nunca
bloqueia a tela -- cai silenciosamente no gradiente.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Frontend — upload de imagem em Configurações → Geral

**Files:**
- Modify: `frontend/src/services/api.ts` (novo `brandingService`)
- Modify: `frontend/src/views/settings/SettingsView.vue` (novo card + lógica de upload/remoção)
- Modify: `frontend/src/i18n/locales/pt-BR.json`
- Modify: `frontend/src/i18n/locales/en.json`

**Interfaces:**
- Consumes: `POST /api/branding/login-background`, `DELETE /api/branding/login-background`, `GET /api/branding` (Tasks 2-4).
- Produces: nada consumido por tarefas seguintes (última tarefa do plano).

- [ ] **Step 1: Adicionar `brandingService` em `api.ts`**

Em `frontend/src/services/api.ts`, logo depois do fechamento do `organizationService` existente (depois da chave que fecha o objeto que contém `uploadOrgAudio`):

```ts
// Branding (system-wide, not per-organization — see docs/superpowers/specs/2026-09-15-login-branding-design.md)
export const brandingService = {
  getPublic: () => api.get('/branding'),
  uploadLoginBackground: (file: File) => {
    const formData = new FormData()
    formData.append('file', file)
    return api.post('/branding/login-background', formData, {
      headers: { 'Content-Type': 'multipart/form-data' }
    })
  },
  deleteLoginBackground: () => api.delete('/branding/login-background')
}
```

- [ ] **Step 2: Adicionar estado e funções no `<script setup>` de `SettingsView.vue`**

Logo depois da importação de `organizationService, usersService` (linha `import { usersService, organizationService } from '@/services/api'`), troque por:

```ts
import { usersService, organizationService, brandingService } from '@/services/api'
```

Logo depois da declaração de `const ringbackAudio = ref<HTMLAudioElement | null>(null)` (perto das outras refs de upload de áudio):

```ts
const loginBackgroundUrl = ref<string | null>(null)
const isUploadingLoginBackground = ref(false)
const isRemovingLoginBackground = ref(false)
const loginBackgroundInput = ref<HTMLInputElement | null>(null)
```

Dentro do `onMounted` existente, adicione a busca ao `Promise.all` já usado ali (troque `Promise.all([organizationService.getSettings(), usersService.me()])` por incluir a terceira chamada):

```ts
onMounted(async () => {
  try {
    const [orgResponse, userResponse, brandingResponse] = await Promise.all([
      organizationService.getSettings(),
      usersService.me(),
      brandingService.getPublic()
    ])

    // ... bloco existente de orgData/user, sem mudança ...

    const brandingData = brandingResponse.data.data || brandingResponse.data
    loginBackgroundUrl.value = brandingData?.login_background_url ?? null
  } catch (error) {
    console.error('Failed to load settings:', error)
  } finally {
    isLoading.value = false
  }
})
```

Depois da função `uploadAudio` existente, adicione:

```ts
async function uploadLoginBackground(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input?.files?.[0]
  if (!file) return

  isUploadingLoginBackground.value = true
  try {
    const response = await brandingService.uploadLoginBackground(file)
    const data = response.data.data || response.data
    loginBackgroundUrl.value = data.login_background_url
    toast.success(t('settings.loginBackgroundUploaded'))
  } catch (error) {
    toast.error(t('settings.loginBackgroundUploadFailed'))
  } finally {
    isUploadingLoginBackground.value = false
    input.value = ''
  }
}

async function removeLoginBackground() {
  isRemovingLoginBackground.value = true
  try {
    await brandingService.deleteLoginBackground()
    loginBackgroundUrl.value = null
    toast.success(t('settings.loginBackgroundRemoved'))
  } catch (error) {
    toast.error(t('settings.loginBackgroundRemoveFailed'))
  } finally {
    isRemovingLoginBackground.value = false
  }
}
```

- [ ] **Step 3: Adicionar o card no template, aba Geral**

Em `frontend/src/views/settings/SettingsView.vue`, dentro de `<TabsContent value="general">`, entre o fechamento do card de "Configurações gerais" e a abertura do card "Meta App Credentials" (a linha em branco depois de `</div>` que fecha o primeiro card, antes de `<!-- Meta App Credentials Card -->`):

```html
            <!-- Login Background Card -->
            <div class="mt-6 rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.loginBackground') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.loginBackgroundDesc') }}</p>
              </div>
              <div class="p-6 pt-3 space-y-3">
                <div v-if="loginBackgroundUrl" class="rounded-lg overflow-hidden border border-white/[0.08] light:border-gray-200 h-32 w-full max-w-sm">
                  <img :src="loginBackgroundUrl" :alt="$t('settings.loginBackground')" class="h-full w-full object-cover" />
                </div>
                <p v-else class="text-sm text-white/50 light:text-gray-500">{{ $t('settings.noFileUploaded') }}</p>
                <div class="flex items-center gap-2">
                  <input ref="loginBackgroundInput" type="file" accept="image/jpeg,image/png,image/webp" class="hidden" @change="uploadLoginBackground" />
                  <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="loginBackgroundInput?.click()" :disabled="isUploadingLoginBackground">
                    <Loader2 v-if="isUploadingLoginBackground" class="mr-2 h-4 w-4 animate-spin" />
                    <Upload v-else class="mr-2 h-4 w-4" />
                    {{ $t('settings.uploadImage') }}
                  </Button>
                  <Button
                    v-if="loginBackgroundUrl"
                    variant="ghost"
                    size="sm"
                    class="text-white/50 hover:text-white light:text-gray-500 light:hover:text-gray-900"
                    @click="removeLoginBackground"
                    :disabled="isRemovingLoginBackground"
                  >
                    <Loader2 v-if="isRemovingLoginBackground" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('common.remove') }}
                  </Button>
                  <span class="text-xs text-white/30 light:text-gray-400">.jpg, .png, .webp (max 5MB)</span>
                </div>
              </div>
            </div>
```

O botão "Remover" reaproveita `common.remove` (`frontend/src/i18n/locales/pt-BR.json:32`, "Remover" — já existe no projeto, confirmado; não crie uma chave nova para isso). `settings.uploadImage` é uma das sete chaves novas do Step 4 abaixo.

- [ ] **Step 4: Adicionar as chaves de i18n**

Em `frontend/src/i18n/locales/pt-BR.json`, depois de `"maskPhoneNumbersDesc": "Ocultar números de telefone exibindo apenas os últimos 4 dígitos",` (linha 610) e antes de `"notifications": "Notificações",`:

```json
    "loginBackground": "Imagem de fundo do login",
    "loginBackgroundDesc": "Substitui o gradiente padrão do painel esquerdo da tela de login por uma imagem",
    "uploadImage": "Enviar imagem",
    "loginBackgroundUploaded": "Imagem de fundo atualizada",
    "loginBackgroundUploadFailed": "Falha ao enviar a imagem",
    "loginBackgroundRemoved": "Imagem de fundo removida",
    "loginBackgroundRemoveFailed": "Falha ao remover a imagem",
```

Em `frontend/src/i18n/locales/en.json`, na mesma posição relativa (depois de `"maskPhoneNumbersDesc"`, antes de `"notifications"`):

```json
    "loginBackground": "Login background image",
    "loginBackgroundDesc": "Replaces the login page's left panel gradient with an uploaded image",
    "uploadImage": "Upload image",
    "loginBackgroundUploaded": "Background image updated",
    "loginBackgroundUploadFailed": "Failed to upload image",
    "loginBackgroundRemoved": "Background image removed",
    "loginBackgroundRemoveFailed": "Failed to remove image",
```

- [ ] **Step 5: Verificar contagem de chaves**

Run (a partir de `frontend/`): `node -e "const a=require('./src/i18n/locales/pt-BR.json'); const b=require('./src/i18n/locales/en.json'); function count(o){return Object.values(o).reduce((n,v)=>n+(typeof v==='object'?count(v):1),0)} console.log('pt-BR:', count(a), 'en:', count(b))"`
Expected: os dois números batem (mesma quantidade de chaves nos dois arquivos) — confirma que as 7 chaves novas foram adicionadas nos dois locales, não só em um.

- [ ] **Step 6: Verificar manualmente**

Com backend e frontend rodando e logado como admin: abra Configurações → Geral. Deve aparecer o card "Imagem de fundo do login" entre o card de Configurações Gerais e o de Meta App Credentials. Clique em "Enviar imagem", escolha um `.jpg` ou `.png` real — o preview deve aparecer, toast de sucesso. Recarregue a página: a imagem enviada deve continuar aparecendo (persistiu de verdade). Abra `/login` numa aba anônima (ou deslogado): o painel esquerdo deve mostrar a imagem enviada. Volte em Configurações → Geral e clique "Remover": preview some, toast de sucesso; recarregue `/login`: volta ao gradiente.

Rode `cd frontend && npm run typecheck` (confirmado: `package.json` já define `"typecheck": "vue-tsc --noEmit"`) — deve dar só os erros pré-existentes do projeto (se houver, ex: `business_calling_enabled`, um erro já conhecido e sem relação com esta mudança), nenhum novo relacionado a `branding`/`loginBackground`.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/services/api.ts frontend/src/views/settings/SettingsView.vue \
        frontend/src/i18n/locales/pt-BR.json frontend/src/i18n/locales/en.json
git commit -m "$(cat <<'EOF'
feat(settings): upload da imagem de fundo do login em Configuracoes -> Geral

Mesmo padrao de UI ja usado pelo upload de musica de espera/ringback:
input de arquivo escondido, botao dispara o upload no @change (nao
num "Salvar" em lote), preview e botao de remover.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review

**Spec coverage** (contra `docs/superpowers/specs/2026-09-15-login-branding-design.md`):
- §2 decisões (escopo do sistema, paleta, painel direito escuro, storage local, frase fixa) → Task 5 (LoginView) e a ausência deliberada de qualquer campo de tagline editável em Task 6.
- §3 modelo de dados (tabela, ID fixo, seed) → Task 1.
- §4 API (4 rotas, contrato mínimo do GET) → Tasks 2-4.
- §4.1 as 5 garantias (sniffing real, escrita atômica, troca de extensão segura, lock cobrindo a sequência inteira, limite de tamanho) → Task 3, com um teste dedicado a cada uma (`RejectsSpoofedContentType`, `RejectsOversizedFile` para a escrita atômica implícita no fluxo, `ExtensionChangeLeavesNoOrphan`). A garantia de concorrência (ponto 4) fica coberta por inspeção de código nos testes de Task 3/4 — nenhum teste dispara duas goroutines concorrentes de verdade; isso é uma lacuna deliberada (testar corrida de verdade é frágil e caro) coberta pelo desenho do código, não por um teste de corrida. Registrado aqui para o revisor de tarefa avaliar se vale a pena um teste de concorrência dedicado.
- §5 frontend (LoginView duas colunas, SettingsView aba Geral) → Tasks 5-6.
- §6 tratamento de erros → coberto pelos testes de cada task (403, 400 por tipo/tamanho, 500 nunca commitando, GET nunca falha a tela).
- §7 verificação → cada item do Go e do manual do spec tem um teste ou um passo manual correspondente nas tarefas.
- §8 fora de escopo → nenhuma task implementa branding por org, tagline editável, S3, ou uso da imagem fora do login — confirmado por omissão deliberada.

**Placeholder scan:** nenhum "TBD"/"TODO". O único texto que soa como aviso ("Se ao rodar go vet... leia como esses helpers realmente constroem uma request... não invente uma segunda convenção") é uma instrução de verificação-antes-de-assumir, não um placeholder de lógica não decidida — o comportamento em si (usar o helper existente) está especificado, só o nome exato do helper depende de uma checagem de uma linha no código real no momento da implementação.

**Consistência de tipos:** `models.BrandingSettings{LoginBackgroundPath, LoginBackgroundContentType}` (Task 1) são os únicos dois campos usados em todo o resto do plano — Tasks 2-4 leem/escrevem exatamente esses dois nomes, nunca inventam um terceiro. `models.BrandingSettingsSingletonID` é usado idêntico em Tasks 1-4. `a.requireAuth(r, models.ResourceSettingsGeneral, models.ActionWrite)` é a mesma chamada em Tasks 3 e 4. O contrato de resposta `{"login_background_url": ...}` é idêntico entre `GetPublicBranding` (Task 2) e o retorno de `UploadLoginBackground` (Task 3) — o frontend (Task 6) lê o mesmo campo dos dois.
