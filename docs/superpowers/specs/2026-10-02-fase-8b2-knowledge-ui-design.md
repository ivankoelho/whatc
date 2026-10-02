# Fase 8B-2: gestão administrativa do Knowledge (UI)

Status: **nota de design para revisão. Nada implementado.** Base: `development` em `73a1fb4` (8B-1 mergeada, PR #79).
Referência: `2026-10-02-fase-8b-knowledge-gestao-rag-design.md` (a 8B inteira) e a API entregue na 8B-1.

## 1. Escopo e fronteira

| Entra na 8B-2 | Fica para a 8B-3 (não entra aqui) |
|---|---|
| aba "Conhecimento" no hub de Serviço | `knowledge.rag_enabled` e a flag por organização |
| lista, filtros, paginação | `Strategy` relaxada (fallback OR) no `Retriever` |
| criar, editar, arquivar, reativar, excluir | montagem de contexto e gancho em `generateAIResponse` |
| painel lateral do documento + chunks | qualquer texto de Knowledge no prompt do chatbot |
| reindexar documento e organização, faixa de status | registro de fontes em `AIUsageLog` |
| teste de busca (modo estrito) | toggle por organização em `ChatbotSettings` |
| upload de texto/Markdown (lido no navegador) | assistente do agente |
| **um** endpoint novo de leitura (seção 3) | embeddings, PDF, migração de `AIContext` |

Fora da 8B-2 também: importação dos manuais pela UI (só CLI, decisão fechada), qualquer mudança de arquitetura da 8B-1,
M8/desempenho. O backend só ganha o endpoint da seção 3.

## 2. Decisões fechadas
Aba no hub de Serviço (`permission: 'knowledge'`); chunks em painel lateral na própria lista (sem página de detalhe);
editor `Textarea` simples + contador + upload de texto/Markdown, limite de 200.000 caracteres, sem editor rico;
importação fora da UI (a tela mostra o estado dos `manual_html` e orienta a CLI); permissões exatamente como na 8B-1;
a UI **não inventa heurística de contexto** (ver seção 7); seletor de escopo por endpoint próprio do Knowledge.

## 3. Único ajuste de backend: `GET /api/knowledge/scopes`

**Por quê.** O formulário e o teste de busca precisam de nomes de unidades/departamentos. Hoje `GET /api/units` e
`/api/departments` exigem `occurrences:read` (e o de unidades devolve campos de outro domínio, como CNPJ e loja X2).
Usá-los acopla Knowledge a Ocorrências e dá a quem só tem `knowledge:*` mais do que precisa. Alternativas descartadas:
(a) reutilizar as rotas atuais e documentar a dependência; (b) abrir essas rotas para `knowledge:write` (altera outro
domínio). O menor ajuste é uma leitura mínima dentro do próprio Knowledge, **sem mudança de schema**, reaproveitando
`knowledgeCapabilities` (a regra da 8B-1).

**Contrato.** `GET /api/knowledge/scopes` — permissão `knowledge:read`; sem parâmetros.

```json
{
  "can_choose_context": true,
  "units":       [{ "id": "uuid", "name": "PORTO SEGURO", "active": true }],
  "departments": [{ "id": "uuid", "name": "Logística",    "active": true }],
  "own":         { "unit_id": "uuid|null", "department_id": "uuid|null" }
}
```

| Quem | `can_choose_context` | `units` / `departments` | `own` |
|---|---|---|---|
| `knowledge:write` (administra) | `true` | **todas** as da organização (ativas e inativas, com `active`) | unidade/departamento do próprio usuário |
| `conversations:view_all` + `knowledge:read` | `true` | todas as da organização | idem |
| só `knowledge:read` | `false` | **somente** a própria unidade e o próprio departamento (para mostrar nomes); vazio se não tiver | idem |

- Só campos `id`, `name`, `active` (nenhum CNPJ, loja X2, endereço). Exclui itens apagados. Ordenado por nome.
- Escopo por organização do usuário; nunca devolve itens de outra organização.
- Erros: `401` sem sessão, `403` sem `knowledge:read`. Nenhum `404`/`409`.
- Implementação prevista: um handler pequeno em `internal/handlers/knowledge_scopes.go` + 1 rota em `main.go`.
  A decisão de "quem escolhe" reusa `knowledgeCapabilities` (sem regra nova).
- Testes Go: matriz acima (admin, view_all, leitor com e sem unidade), isolamento entre organizações, itens apagados
  fora, inativos marcados, `403` sem permissão, ausência de campos extras.

## 4. Endpoints existentes consumidos (8B-1, sem alteração)

| Uso | Chamada | Permissão |
|---|---|---|
| lista | `GET /knowledge/documents?page&limit&search&source_type&status&unit_id&department_id` → `{documents,total,page,limit}` (sem `body`) | read |
| leitura | `GET /knowledge/documents/{id}` (com `body`) | read |
| criar | `POST /knowledge/documents` | write |
| editar/arquivar/reativar | `PUT /knowledge/documents/{id}` | write |
| excluir | `DELETE /knowledge/documents/{id}` | write |
| chunks | `GET /knowledge/documents/{id}/chunks` → `{chunks,total}` | read |
| reindexar documento | `POST /knowledge/documents/{id}/reindex` → `{chunks,index_version}` | write |
| reindexar organização | `POST /knowledge/reindex?only_stale=true\|false` → `{documents,chunks,skipped,stale_remaining}` | write |
| status do índice | `GET /knowledge/status` → `{documents,chunks,stale_chunks,index_version}` | write |
| teste de busca | `GET /knowledge/search?q&unit_id&department_id&limit` → `{results}` | read |
| nomes de escopo | `GET /knowledge/scopes` (nova, seção 3) | read |

## 5. Contratos TypeScript (`frontend/src/services/api.ts`)

```ts
export type KnowledgeSourceType = 'faq' | 'article' | 'process' | 'text' | 'markdown' | 'manual_html'
export type KnowledgeStatus = 'active' | 'archived'
export type KnowledgeArchivedBy = '' | 'user' | 'import'
export type KnowledgeVisibility = 'organization' | 'unit' | 'department'

export interface KnowledgeDocument {
  id: string; organization_id: string
  unit_id: string | null; department_id: string | null
  visibility: KnowledgeVisibility; source_type: KnowledgeSourceType
  title: string; origin?: string; body?: string          // body só no GET por id
  content_hash?: string; status: KnowledgeStatus; archived_by: KnowledgeArchivedBy
  created_at: string; updated_at: string
}
// PUT é a representação completa: unit_id e department_id SEMPRE presentes (UUID ou null).
export interface KnowledgeDocumentInput {
  title?: string; body?: string; source_type?: Exclude<KnowledgeSourceType, 'manual_html'>
  unit_id: string | null; department_id: string | null
  status?: KnowledgeStatus; expected_updated_at?: string
}
export interface KnowledgeChunk { id: string; chunk_index: number; heading: string; content: string; char_count: number; index_version: number }
export interface KnowledgeCitation { title: string; heading?: string; source_type: KnowledgeSourceType; origin?: string; anchor?: string }
export interface KnowledgeHit { document_id: string; chunk_id: string; title: string; heading?: string; content: string; score: number; citation: KnowledgeCitation }
export interface KnowledgeScopeItem { id: string; name: string; active: boolean }
export interface KnowledgeScopes { can_choose_context: boolean; units: KnowledgeScopeItem[]; departments: KnowledgeScopeItem[]; own: { unit_id: string | null; department_id: string | null } }
export interface KnowledgeIndexStatus { documents: number; chunks: number; stale_chunks: number; index_version: number }
export interface KnowledgeReindexResult { documents: number; chunks: number; skipped: number; stale_remaining: number }

export const knowledgeService = {
  list(params), get(id), create(body), update(id, body), remove(id),
  chunks(id), reindexDocument(id), reindex(onlyStale), status(), search(params), scopes(),
}
```

Funções puras testáveis em `frontend/src/lib/knowledge.ts` (sem Vue): rótulo de escopo, classificação de erro
(`409|403|404|400`), montagem do corpo do PUT (sempre as duas chaves; `manual_html` só escopo/status), validação do
formulário (título ≤ 500, corpo não vazio e ≤ 200.000 caracteres), leitura de arquivo (somente `.txt`/`.md`/`text/*`,
≤ 200.000 caracteres).

## 6. Matriz de permissões na tela

| Elemento | read | read + view_all | write |
|---|---|---|---|
| aba "Conhecimento" visível | sim | sim | sim |
| lista (documentos ativos do próprio contexto) | sim | sim (do contexto escolhido) | sim (todos, arquivados incluídos) |
| filtros tipo/título | sim | sim | sim |
| filtro unidade/departamento | **não** | **sim, rotulado "Contexto"** | **sim, rotulado "Escopo"** (igualdade) |
| filtro de status / arquivados | não | não | sim |
| abrir painel (conteúdo + chunks) | sim | sim | sim |
| teste de busca | sim, sem seletor de contexto | sim, com seletor | sim, com seletor |
| criar, editar, arquivar, reativar, excluir | **não aparecem** | não aparecem | sim |
| reindexar, faixa de status | **não aparecem** | não aparecem | sim |

Os controles de escrita simplesmente não existem no DOM sem `knowledge:write` (`authStore.hasPermission('knowledge','write')`).
O frontend **nunca chama** `status`/`reindex` sem `write` (evita `403` previsível). A autorização real continua no backend.

## 7. Contexto (leitura contextual × escolha de contexto), sem heurística nova

- `can_choose_context = false` (só `read`): nenhum seletor. A tela mostra o contexto fixo ("Seu contexto: Unidade X /
  Depto Y" ou "somente conteúdo global" se `own` vazio) e **não envia** `unit_id`/`department_id` (o servidor usa o do usuário).
- `can_choose_context = true`: seletores de unidade e departamento, opcionais; **sem valor padrão** (vazio = sem unidade/
  departamento = só global, exatamente a semântica da 8B-1). A UI não preenche o contexto com o do usuário nem infere nada.
- Para `write` o filtro da lista é de **escopo** (documentos com aquela unidade/departamento); para `view_all` sem `write`
  é de **contexto** (o que é elegível). O rótulo muda para refletir isso.
- Parâmetro fora do alcance (`403`) → mensagem "Contexto fora do seu alcance"; unidade/departamento inexistente (`400`)
  → mensagem de validação. Nada é ignorado em silêncio.

## 8. Estados da tela (aba)

`loading` (esqueleto) → `error` (`ErrorState` com "Tentar de novo", mantém filtros) → `empty` (sem documentos: texto +
orientação da CLI; `write` vê também "Novo documento") → `filtered-empty` ("nenhum resultado para os filtros", botão limpar) →
`list`. Sobrepostos: faixa de índice obsoleto (write, `stale_chunks > 0`), faixa informativa dos manuais, estados de
envio (`saving`, `reindexing`) que desabilitam o botão e mostram spinner. Documentos `manual_html` mostram selo
"Manual" e, quando arquivados, "arquivado pela importação" ou "arquivado por usuário" conforme `archived_by`.
A orientação da CLI é só texto (`whatomate knowledge import-manuals -org <id>`), sem botão que execute nada.

## 9. Comportamento de cada operação

| Operação | Chamada | Sucesso | `400` | `403` | `404` | `409` |
|---|---|---|---|---|---|---|
| Listar | `GET /documents` | tabela | mensagem (UUID de filtro inválido) | "sem permissão", aba mostra aviso | — | — |
| Abrir painel | `GET /documents/{id}` + `GET …/chunks` | conteúdo e chunks | — | fecha o painel, aviso | **fecha o painel**, "documento não está mais disponível", recarrega a lista | — |
| Criar | `POST` | fecha o diálogo, toast, recarrega, abre o painel do novo | mostra a mensagem do servidor no formulário (título/corpo/tipo/escopo) | toast; botões já ocultos | — | — |
| Editar | `GET {id}` (corpo + `updated_at`) → `PUT` com `expected_updated_at` | toast, atualiza lista e painel | mensagem no formulário | toast | fecha, avisa, recarrega | **diálogo de conflito** "alterado por outra pessoa": *Recarregar* (descarta e relê) ou *Cancelar*; não sobrescreve |
| Editar `manual_html` | `PUT` só `unit_id`/`department_id`/`status` | idem | idem | idem | idem | só se o servidor recusar conteúdo; a UI nunca envia título/corpo |
| Arquivar / reativar | `GET {id}` → `PUT` completo com o `status` novo (`manual_html`: só escopo+status) | toast, a linha muda | — | toast | recarrega | diálogo de conflito |
| Excluir | `DELETE` (confirmação; para `manual_html` o texto avisa que a importação recria) | toast, recarrega, fecha o painel | — | toast | trata como já excluído (sucesso) | — |
| Reindexar documento | `POST …/reindex` | toast "N chunks", recarrega chunks e status | — | toast | fecha o painel, recarrega | — |
| Reindexar obsoletos / tudo | `POST /reindex?only_stale=…` (tudo pede confirmação) | toast com `{documents,chunks,skipped,stale_remaining}`, recarrega status e lista | `only_stale` inválido (não ocorre) | toast | — | — |
| Teste de busca | `GET /search` | resultados | `q` vazio é barrado no cliente; `400` do servidor vira mensagem | "Contexto fora do seu alcance" | — | — |

Regras gerais: toda mensagem vem de i18n, com o texto do servidor anexado quando útil (`getErrorMessage`); nenhuma
operação fica "pendurada" (spinner sempre limpo no `finally`); depois de qualquer escrita a lista e a faixa de status são relidas.
`PUT` sempre envia `unit_id` **e** `department_id` (UUID ou `null`); o formulário nunca monta um corpo sem as duas chaves.

## 10. Paginação e filtros (lista)

- Paginação do servidor (`page`, `limit`; `limit` padrão 20 na UI, opções 20/50/100; o servidor limita em 100) com
  `PaginationControls`. Mudou qualquer filtro → volta à página 1. Ordenação é a do servidor (título A→Z).
- Filtros: busca por título (debounce 300 ms; `search`), tipo (`source_type`), status e escopo/contexto conforme a matriz
  da seção 6. "Limpar filtros" restaura tudo.
- Estado dos filtros fica no componente (não na URL, exceto `?tab=knowledge` do hub). Resposta antiga não sobrescreve
  a nova (descarta resultado de requisição substituída).
- A lista **não traz o corpo** (o servidor omite); o corpo vem do `GET` por id ao abrir ou editar.

## 11. Painel lateral do documento e contrato dos chunks

Um `Sheet` à direita da lista, aberto ao clicar numa linha ou num resultado do teste de busca.
- **Cabeçalho:** título, selos (tipo, status, `manual_html`), escopo, `updated_at`; para `write`, botões Editar, Arquivar/
  Reativar, Reindexar este documento, Excluir.
- **Aba "Conteúdo":** corpo somente leitura (texto simples, quebras preservadas), `origin` quando houver; em `manual_html`,
  aviso "conteúdo gerenciado pela importação: altere o HTML e importe de novo".
- **Aba "Chunks":** `GET /documents/{id}/chunks`, na ordem de `chunk_index`; cada item mostra `#índice`, `heading`,
  `char_count`, `index_version` e o `content` (recolhível). Selo "obsoleto" quando `index_version < status.index_version`
  (só `write` conhece o status; sem `write` o selo mostra apenas o número). Zero chunks → texto explicativo (+ botão
  "Reindexar" para `write`). `404` fecha o painel (seção 9).
- Sem scroll horizontal; no celular o painel ocupa a largura toda.

## 12. Contrato do teste de busca

Painel dentro da própria aba (colapsável): campo `q`, seletores de unidade/departamento **só se** `can_choose_context`,
limite (5/10/20), botão Buscar (envio explícito, sem busca a cada tecla). Modo **estrito** apenas (a 8B-3 trará o relaxado; a
API não o expõe).
- Chamada: `GET /knowledge/search?q&limit[&unit_id&department_id]`; só envia os seletores preenchidos.
- Resultado: título, `heading`, trecho (`content` com limite visual), **score** com tooltip "relevância lexical,
  comparável só dentro desta resposta; não é uma probabilidade", citação (`source_type`, `origin`, âncora). Clique abre o painel do documento.
- Estados: inicial (instrução), carregando, vazio ("nenhum resultado"; dica de que palavras muito comuns são ignoradas),
  erro, `403` ("contexto fora do seu alcance"). `q` em branco desabilita o botão.
- Não há realce de termos, paginação nem fallback OR.

## 13. i18n

Chaves novas em `en.json` e `pt-BR.json`, sem texto fixo nos componentes: `nav.knowledge` (aba), namespace `knowledge.*`
(títulos, colunas, tipos, status, "arquivado pela importação/por usuário", escopos, filtros, estados vazios, orientação da
CLI, formulário e validações, contador de caracteres, upload, confirmações de exclusão/reindex, conflito, toasts de
sucesso/erro, painel, teste de busca e tooltip do score). Verificar na implementação se a tela de papéis precisa de rótulo
para o recurso `knowledge` (se houver mapa de nomes de permissão) e adicioná-lo.

## 14. Arquivos que serão alterados

Backend (somente o ajuste da seção 3): `internal/handlers/knowledge_scopes.go` (novo), `internal/handlers/knowledge_scopes_test.go`
(novo), `cmd/whatomate/main.go` (+1 rota).
Frontend: `src/services/api.ts` (tipos + `knowledgeService`); `src/lib/knowledge.ts` e `src/lib/knowledge.spec.ts` (novos);
`src/views/settings/KnowledgeView.vue` (novo); `src/components/knowledge/KnowledgeDocumentDialog.vue`,
`KnowledgeDocumentPanel.vue`, `KnowledgeSearchPanel.vue`, `KnowledgeIndexBar.vue` (novos);
`src/views/settings/ServiceSettingsHubView.vue` (aba); `src/router/index.ts` (item de permissão no mapa de navegação);
`src/i18n/locales/en.json` e `pt-BR.json`. Nenhum outro arquivo.

## 15. Testes e verificação

- **Go:** `knowledge_scopes_test.go` (seção 3). `go build`, `go vet`, suíte completa (só as 3 falhas conhecidas).
- **Vitest (`src/lib/knowledge.spec.ts`):** corpo do PUT sempre com as duas chaves (UUID e `null`); `manual_html` só envia
  escopo/status; validação (título, corpo vazio, 200.000 caracteres); leitura de arquivo (extensões, tamanho); rótulos de
  escopo e de quem arquivou; classificação de `409/403/404/400`; regra "controles de escrita só com `write`".
- **Estático:** `vue-tsc`, ESLint nos arquivos alterados, `npm run build`.
- **Smoke visual** (como na 3B): banco descartável, backend isolado (porta própria, Redis em outro DB, `-workers 0`),
  Vite temporário, login com o admin semeado do projeto (só teste local); manuais importados pela CLI; para simular
  índice obsoleto, `index_version = 1` por SQL no banco descartável. Roteiro: lista/filtros/paginação; criar (todos os
  tipos), upload `.md`, contador e limite; editar com **conflito real** (segunda alteração via API entre a leitura e o
  salvamento); arquivar/reativar; `manual_html` somente leitura e aviso da CLI; painel + chunks (obsoleto); reindexar
  documento e obsoletos; teste de busca (acentos, composto `não-conformidade`, duas frases, vazio, `403` de contexto);
  excluir; usuário só `knowledge:read` (sem controles de escrita, sem seletor, sem chamadas a `status`) e usuário
  `view_all` (seletor de contexto, sem arquivados); tema claro/escuro e largura de celular; console sem erros novos.
  Tudo é derrubado depois (servidores, banco, Redis, configs temporárias). Nada no banco real, nenhuma chamada ao X2.

## 16. Riscos

- Reindexação organizacional é síncrona (decisão da 8B-1): a UI mostra spinner e bloqueia o botão; se o tempo de
  resposta crescer, vira tarefa em fila (fora da 8B-2).
- `PUT` exige `GET` prévio para arquivar/reativar (precisa de título/corpo): custo de uma requisição extra; aceito.
- Documentos muito grandes (até 200.000 caracteres) no painel: corpo exibido em área rolável, sem destaque de sintaxe.

## 17. Pontos para revisão
1. O contrato de `GET /api/knowledge/scopes` (seção 3), em especial: leitor comum recebe só a própria unidade/departamento;
   quem pode escolher recebe todos; inclui inativos marcados; só `id`, `name`, `active`.
2. Rótulo "Escopo" (write) × "Contexto" (view_all) para o mesmo par de seletores (seção 7).
3. Arquivar/reativar via `GET` + `PUT` completo (sem endpoint novo de status) (seção 9).
4. Estado dos filtros apenas em memória (sem sincronizar com a URL).

## 18. Implementação: desvios da nota e achados da verificação visual

Desvios (todos pequenos, registrados aqui):
1. **Arquivar/reativar e conflito:** a nota previa o diálogo de conflito também para arquivar. Como a versão é lida imediatamente
   antes do PUT, o conflito virou um aviso (toast) com recarga da lista; o diálogo de conflito existe na edição, onde há janela real.
2. **Arquivos fora da lista da seção 14:** `frontend/src/components/layout/navigation.ts` (duas listas de permissões do menu).
3. **PageHeader sem `description`:** o cabeçalho tem altura fixa e a descrição longa sobrepunha o título no celular; a descrição
   fica no cartão. Em tela estreita a tabela mostra título, tipo, status e ações (escopo e data ficam no painel).
4. **Rótulos do rodapé do diálogo** passaram por i18n (`common.cancel/create/update`) e as mensagens de validação seguem o campo.
5. Acréscimos de teste: `KnowledgeScopes` agora também tem testes de isolamento por ids diretos (organização A × B).

Achados da verificação visual (corrigidos antes do commit):
- **PUT 400 ao arquivar/editar documento global:** o servidor omite `unit_id`/`department_id` quando são `null`
  (`omitempty`); o frontend repassava `undefined`, que o JSON descarta, e o PUT exige as duas chaves. Os tipos passaram a
  `unit_id?: string | null`, os montadores de corpo enviam sempre `null` explícito, e há specs que serializam o corpo (JSON
  real) para provar as duas chaves.
- **Redirecionamento infinito do roteador** para quem só tem `knowledge:read`: faltava `knowledge` em `meta.anyPermission` da rota
  do hub e nas duas listas do menu. Corrigido nos três pontos.

Pontos conhecidos, não corrigidos nesta fase: a mensagem do banner de índice obsoleto não tem plural ("1 trechos").
