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
| `login_background_content_type` | string, nulo | Tipo real detectado por sniffing do conteúdo (ver §4.1) — nunca o `Content-Type` que o cliente mandou |

`BaseModel` padrão, mas com um desvio deliberado: o `ID` **não** usa o default `gen_random_uuid()` — é semeado com um UUID fixo e conhecido em tempo de migração (`00000000-0000-0000-0000-000000000001`, mesmo valor sempre). Isso elimina de vez a pergunta "que linha é a linha certa": não existe get-or-create em request nenhum, só get-by-id-fixo. A linha nasce uma única vez, no boot com `-migrate`, junto dos outros seeds que já existem (`CreateDefaultAdmin`, os backfills de permissão) — inserção via `clause.OnConflict{DoNothing: true}` no PK, o mesmo idioma já usado em `ensureDefaultSLAPolicies` (Fase 3 de Ocorrências) para semear com segurança sob concorrência, sem precisar de índice único adicional (o PK já garante unicidade). Se a seed nunca rodou (deploy antigo sem `-migrate`), os handlers tratam "linha não encontrada" como erro 500 explícito — não deveria acontecer em operação normal, e não vale a pena um get-or-create silencioso só para esse caso extremo.

## 4. API

```
GET    /api/branding                    -- público, sem auth
GET    /api/branding/login-background   -- público, sem auth (serve o arquivo)
POST   /api/branding/login-background   -- settings.general:write, multipart upload
DELETE /api/branding/login-background   -- settings.general:write
```

(Correção pós-validação no código: o spec original tinha `POST`/`DELETE` sob um prefixo `/api/settings/branding/...` que não existe em nenhum outro lugar do backend — as configurações "gerais" hoje vivem em `/api/chatbot/settings`, e endpoints como `/api/units` já usam o mesmo path pra GET público e POST/DELETE autenticado, diferenciando só pelo verbo. `/api/branding/login-background` segue essa convenção real em vez de inventar uma nova.)

- `GET /api/branding`: contrato mínimo, só um campo, sempre presente:
  ```json
  {"login_background_url": null}
  ```
  ou
  ```json
  {"login_background_url": "/api/branding/login-background?v=1757930000"}
  ```
  Nunca `created_at`, `updated_at`, `id` nem qualquer outro campo interno da tabela — só o suficiente pro frontend decidir "tem imagem ou não" e montar a URL. O `?v=` é o unix timestamp de `updated_at`, cache-busting simples para o navegador não servir uma imagem trocada a partir do cache HTTP.
- `GET /api/branding/login-background`: serve o arquivo com o `Content-Type` salvo (o sniffado, não o do header de upload), mesma proteção contra path traversal/symlink já usada em `ServeMedia` (`internal/handlers/media.go`). 404 se não houver imagem configurada.
- `POST .../login-background`: aceita `multipart/form-data`, campo `file`. Ver §4.1 para a sequência completa de validação, escrita atômica e troca segura do arquivo.
- `DELETE .../login-background`: mesma seção de lock/transação do upload (§4.1), só que zerando os dois campos e removendo o arquivo após o commit.

Nenhum dos dois endpoints de leitura pública expõe nada além da imagem em si — sem vazamento de dados de organização, usuário ou config sensível.

## 4.1 Upload — sequência atômica, com validação real e segura sob concorrência

Cinco garantias, cada uma resolvendo um problema concreto:

1. **Validação por conteúdo, não por header.** O `Content-Type` que o multipart manda é só um chute do cliente — `internal/handlers/campaigns.go:883-896` hoje confia nele, e é exatamente esse ponto fraco que este endpoint não repete. Os bytes lidos passam por `http.DetectContentType` (stdlib `net/http`, sem dependência nova) logo após a leitura; só o tipo *detectado* é aceito contra a lista `{image/jpeg, image/png, image/webp}` (mesmo subconjunto de imagens que `campaigns.go` já aceita) e é esse tipo detectado — nunca o do header — que vira `login_background_content_type` no banco e o `Content-Type` servido depois.
2. **Escrita atômica.** Segue literalmente o padrão de `internal/tts/piper.go:72-93`: grava em `<caminho-final>.tmp` primeiro, e só faz `os.Rename(tmp, final)` depois da escrita completa. `os.Rename` no mesmo diretório é atômico no nível do filesystem — o processo nunca observa um arquivo pela metade. Se o processo for interrompido antes do rename, o `.tmp` fica órfão (removido na próxima tentativa) e o arquivo antigo continua intacto e servindo.
3. **Extensão pode mudar sem período de dois arquivos coexistindo como "o atual".** O nome final já embute a extensão detectada (`login-background.jpg` num upload, `login-background.png` no próximo) — reaproveita `getExtensionFromMimeType` (`internal/handlers/media.go:33`). O arquivo da extensão antiga só é apagado **depois** que a transação abaixo confirma (`COMMIT`) que o banco já aponta pro novo caminho. Nunca antes. Se o processo cair entre o rename do novo arquivo e o commit, o pior caso é um arquivo órfão da extensão nova sobrando no disco — nunca perda da imagem que já estava no ar.
4. **Concorrência: tudo sob um lock de linha, do início ao fim.** Não é só a atualização do banco que precisa ser serializada — é a sequência inteira (ler caminho antigo → escrever novo arquivo → apontar banco pro novo → só então apagar o antigo). Por isso o lock é tomado **antes** da escrita do arquivo, não só na hora do `UPDATE`:
   ```go
   tx := a.DB.Begin()
   var row models.BrandingSettings
   tx.Clauses(clause.Locking{Strength: "UPDATE"}).
       Where("id = ?", brandingSettingsSingletonID).First(&row)   // mesmo idioma de agent_transfers.go:1107 (sem SKIP LOCKED -- aqui não é fila, é singleton: a segunda requisição deve ESPERAR e pegar o estado fresco, não pular)
   oldPath := row.LoginBackgroundPath
   // ... sniff, escreve .tmp, os.Rename para o caminho final (extensão detectada) ...
   tx.Model(&row).Updates(map[string]any{
       "login_background_path":         newRelPath,
       "login_background_content_type": sniffedType,
   })
   tx.Commit()
   if oldPath != "" && oldPath != newRelPath {
       _ = os.Remove(filepath.Join(basePath, oldPath)) // best-effort, não falha a resposta -- o banco já é a fonte da verdade
   }
   ```
   Uma segunda requisição concorrente bloqueia no `FOR UPDATE` até a primeira commitar; quando destrava, já enxerga o `oldPath` atualizado pela primeira — as duas nunca decidem "qual arquivo é o atual" com informação desatualizada, e os dois arquivos finais (se as extensões diferirem) nunca coexistem como ponteiro válido ao mesmo tempo. Sem índice único novo, sem mutex de processo — só o lock de linha que `agent_transfers.go` já usa em produção para exatamente esse tipo de corrida.
5. **Limite de tamanho.** 5MB, validado antes de qualquer escrita em disco (`io.LimitReader`, mesmo padrão de `campaigns.go:871-880`, só que menor porque isso é uma imagem de fundo, não mídia de campanha).

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

- Arquivo cujo conteúdo sniffado não é um dos três tipos aceitos → 400, mensagem clara. Vale mesmo que o header do multipart diga `image/jpeg` — é o conteúdo que decide, não o header (§4.1, ponto 1).
- Upload maior que 5MB → 400, antes de qualquer escrita em disco.
- Falha ao escrever o `.tmp` ou ao fazer o rename (permissão, disco cheio) → 500, log do erro, transação nunca chega a commitar — `branding_settings` continua apontando pro arquivo anterior, que continua servindo normalmente.
- Falha ao remover o arquivo antigo depois do commit → log do erro, mas a resposta ao cliente já foi 200 (o banco já está correto e é a fonte da verdade; um arquivo órfão no disco não é visível a ninguém, é higiene, não correção).
- `GET /api/branding` nunca deve derrubar a tela de login: qualquer erro do lado do frontend ao buscar essa config (rede, 500, JSON malformado) é engolido e tratado como "sem imagem configurada" — o frontend trata explicitamente `login_background_url === null` como "sem imagem" e qualquer falha na própria chamada do mesmo jeito, nunca deixando a tela de login travada esperando essa config opcional.

## 7. Verificação

**Go**
- Upload aceito para `settings.general:write`, rejeitado (403) para quem não tem essa permissão.
- Upload com `Content-Type: image/jpeg` no header mas conteúdo que não é uma imagem de verdade (ex: um `.txt` renomeado) → rejeitado (400) — prova que a validação é por sniffing, não por header (mockar um header mentiroso é o teste que realmente prova o ponto 1 do §4.1).
- Upload acima do limite de tamanho rejeitado (400), sem nenhum arquivo `.tmp` sobrando no disco.
- Upload de jpg seguido de upload de png: depois do segundo, `login-background.jpg` não existe mais no disco e `login-background.png` é o único arquivo de fundo presente — prova o ponto 3 do §4.1 (extensão muda sem período de dois arquivos válidos).
- Duas goroutines fazendo upload concorrente (um jpg, um png) contra a mesma linha: ao final, o arquivo apontado por `branding_settings.login_background_path` é o que corresponde ao upload que venceu a corrida do lock, e é o único arquivo de imagem restante no diretório `branding/` — prova o ponto 4 do §4.1 (lock cobre a sequência inteira, não só o UPDATE).
- `GET /api/branding` sem autenticação alguma retorna 200 com exatamente o campo `login_background_url` (nenhum outro campo no JSON) — endpoint público de verdade, contrato mínimo confirmado.
- `GET /api/branding/login-background` serve o `Content-Type` sniffado (não o do header original do upload) e nega path traversal (mesmo teste de segurança que já existe para `ServeMedia`).
- `DELETE` remove o arquivo do disco só depois do commit e volta o `GET /api/branding` a `{"login_background_url": null}`.
- Interromper o processo (ou simular falha) entre a escrita do `.tmp` e o `os.Rename`: o arquivo final antigo continua intacto e servindo — prova o ponto 2 do §4.1 (atomicidade).

**Manual (navegador)**
- Login sem nenhuma imagem configurada → gradiente verde.
- Após upload → painel esquerdo mostra a imagem, painel direito continua funcional (login de verdade funciona).
- Após remover → volta ao gradiente.

## 8. Fora de escopo (explicitamente)

- Branding por organização (exigiria seletor de org no login).
- Frase de efeito editável via settings.
- Suporte a S3 para esse upload especificamente.
- Qualquer outra tela além do login usar essa imagem (ex: e-mail transacional, PDF de protocolo) — não foi pedido.
