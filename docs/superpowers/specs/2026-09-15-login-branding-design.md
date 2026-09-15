# Tela de login com painel de marca configurável

- **Data:** 2026-09-15
- **Status:** Aprovada — pronta para plano de implementação
- **Autor/revisão:** Ivan Coelho (product) · design colaborativo

## 1. Contexto

O pedido: redesenhar a tela de login (`frontend/src/views/auth/LoginView.vue`) num layout de duas colunas — inspirado numa referência visual (protótipo "Centro de Controle", ver [[centro-de-controle-lovable-prototype]] na memória do projeto) — e permitir que um administrador troque o painel esquerdo por uma imagem enviada via upload, em vez do gradiente padrão.

Hoje a tela de login é um card único centralizado, sem nenhum conceito de marca/logo configurável. O app é multi-tenant no modelo de dados (`Organization`), mas a tela de login não tem seletor de organização — o usuário entra só com e-mail/senha, e a organização é resolvida a partir do usuário autenticado. Não existe hoje nenhuma tabela ou configuração de "branding" no backend.

## 2. Decisões

| Questão | Decisão | Motivo |
|---|---|---|
| Escopo do branding | Configuração única do sistema, não por organização | Não existe seletor de org no login; a imagem precisa ser resolvida antes de saber quem é o usuário. Multi-tenant de verdade exigiria adicionar esse seletor primeiro — fora de escopo. |
| Paleta | Mantém verde/emerald da marca atual; só adota a *estrutura* de duas colunas da referência | O app é dark-first em tudo; a referência usa fundo claro. Trocar a paleta só na tela de login quebraria a identidade visual do resto do produto. |
| Painel direito (form) | Continua escuro, mesmo estilo do card atual, só reposicionado | Menor mudança visual possível fora do painel esquerdo, que é o que foi pedido. |
| Armazenamento da imagem | Tabela nova `branding_settings` (linha única) + arquivo em disco local, mesmo padrão de `campaigns.go`/`media.go` | Evita misturar uma config de sistema dentro do `Settings` JSONB de uma organização escolhida arbitrariamente; seguir o padrão de upload local já usado no resto do backend evita reinventar mecanismo de storage. |
| Frase de efeito do painel esquerdo | Fixa no frontend, não editável via settings | Só o upload de imagem foi pedido; editar texto de marca é escopo novo, não pedido — YAGNI. |
| Tipo de storage | Só local (`storage.type = "local"`) | O upload de mídia de campanha já funciona assim hoje, sem branch para S3; não introduzir suporte a S3 aqui seria inconsistente com o que já existe. |

## 3. Modelo de dados

Uma tabela nova, linha única (sem `organization_id` — é config de sistema, não de tenant).

### `branding_settings`

| Campo | Tipo | Observação |
|---|---|---|
| `login_background_path` | string, nulo | Caminho relativo dentro de `storage.local_path`, ex: `branding/login-background.jpg` |
| `login_background_content_type` | string, nulo | `image/jpeg`, `image/png`, etc. — usado ao servir o arquivo |

`BaseModel` padrão (id, created_at, updated_at, deleted_at). Leitura sempre pega a primeira linha (`ORDER BY created_at ASC LIMIT 1`); se não existir nenhuma, cria uma vazia na primeira escrita — mesmo padrão preguiçoso de `ensureDefaultStages`/`ensureDefaultSLAPolicies`, mas aqui garantindo no máximo uma linha (sem necessidade de índice único parcial: só o backend escreve nela, nunca em paralelo por múltiplas organizações).

## 4. API

```
GET    /api/branding                    -- público, sem auth
GET    /api/branding/login-background   -- público, sem auth (serve o arquivo)
POST   /api/settings/branding/login-background    -- settings.general:write, multipart upload
DELETE /api/settings/branding/login-background     -- settings.general:write
```

- `GET /api/branding`: devolve `{"login_background_url": "/api/branding/login-background?v=<updated_at>"}` quando configurado, ou `{"login_background_url": null}` quando não. O `?v=` é cache-busting simples (timestamp da última atualização), evita o navegador servir uma imagem trocada a partir do cache do HTTP.
- `GET /api/branding/login-background`: serve o arquivo com `Content-Type` salvo, mesma proteção contra path traversal/symlink já usada em `ServeMedia` (`internal/handlers/media.go`). 404 se não houver imagem configurada.
- `POST .../login-background`: aceita `multipart/form-data`, campo `file`. Valida: presente, `Content-Type` começa com `image/`, tamanho ≤ 5MB (limite novo — não existe precedente explícito no código atual para uploads de mídia, mas é um valor sensato para uma imagem de fundo). Salva em `<storage.local_path>/branding/login-background.<ext>`, sobrescrevendo o arquivo anterior se existir. Atualiza (ou cria) a linha única de `branding_settings`.
- `DELETE .../login-background`: remove o arquivo do disco (se existir) e limpa os dois campos da linha — a tela de login volta a mostrar o gradiente padrão.

Nenhum dos dois endpoints de leitura pública expõe nada além da imagem em si — sem vazamento de dados de organização, usuário ou config sensível.

## 5. Frontend

### `LoginView.vue`

Reestruturado em duas colunas com um breakpoint responsivo padrão Tailwind (`lg:`) — não existe um precedente desse padrão em outra tela do app (a tela de login é a única com esse layout de duas colunas), então é uma decisão nova, não a aplicação de uma convenção já existente. Colapsa para coluna única (só o card de login) em telas estreitas:

- **Painel esquerdo** (escondido em telas estreitas): gradiente verde/emerald por padrão (reaproveita as cores já usadas no ícone/botão da tela atual); se `GET /api/branding` devolver uma URL, essa imagem substitui o gradiente como `background-image`, com um overlay escuro sutil para o texto continuar legível em qualquer imagem. Logo do app + frase de efeito fixa sobrepostos.
- **Painel direito**: o card de login atual (e-mail, senha, SSO, link de registro), sem mudança de conteúdo — só deixa de estar centralizado sozinho na tela e passa a ocupar a coluna direita.

A chamada a `GET /api/branding` acontece no `onMounted`, em paralelo com a busca de provedores SSO que já existe. Se a chamada falhar (erro de rede, 500), cai silenciosamente no gradiente padrão — a tela de login nunca pode ficar bloqueada ou quebrada por causa dessa configuração opcional.

### `SettingsView.vue` — aba Geral

Novo campo na seção de Configurações Gerais existente: um input de upload de imagem com preview da imagem atual (se houver) e botão "Remover" para voltar ao padrão. Usa os dois endpoints autenticados. Mensagens de sucesso/erro seguem o padrão de toast já usado no resto da tela (`saveGeneralSettings`).

Chaves de i18n novas em `pt-BR.json`/`en.json`, na mesma posição relativa nos dois arquivos (convenção já existente no projeto).

## 6. Tratamento de erros

- Upload de arquivo não-imagem → 400, mensagem clara.
- Upload maior que 5MB → 400.
- Falha ao escrever no disco (permissão, disco cheio) → 500, log do erro, config não é atualizada (o arquivo antigo continua servindo até uma tentativa bem-sucedida).
- `GET /api/branding` nunca deve derrubar a tela de login: qualquer erro do lado do frontend ao buscar essa config é engolido e tratado como "sem imagem configurada".

## 7. Verificação

**Go**
- Upload aceito para `settings.general:write`, rejeitado (403) para quem não tem essa permissão.
- Upload de arquivo não-imagem rejeitado (400).
- Upload acima do limite de tamanho rejeitado (400).
- `GET /api/branding` sem autenticação alguma retorna 200 (endpoint público de verdade, não só "não checado").
- `GET /api/branding/login-background` serve o `Content-Type` correto e nega path traversal (mesmo teste de segurança que já existe para `ServeMedia`).
- `DELETE` remove o arquivo do disco e volta o `GET /api/branding` a `null`.
- Segunda chamada de upload sobrescreve a primeira (sem acumular arquivos órfãos no disco).

**Manual (navegador)**
- Login sem nenhuma imagem configurada → gradiente verde.
- Após upload → painel esquerdo mostra a imagem, painel direito continua funcional (login de verdade funciona).
- Após remover → volta ao gradiente.

## 8. Fora de escopo (explicitamente)

- Branding por organização (exigiria seletor de org no login).
- Frase de efeito editável via settings.
- Suporte a S3 para esse upload especificamente.
- Qualquer outra tela além do login usar essa imagem (ex: e-mail transacional, PDF de protocolo) — não foi pedido.
