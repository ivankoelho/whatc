# Settings tab hubs — design spec

**Status:** aguardando revisão do usuário. Não implementar até aprovação explícita.

## Natureza da mudança

Isto é uma mudança **estrutural/navegacional** no frontend. Nenhuma lógica funcional das 16 telas envolvidas muda: os mesmos componentes, os mesmos serviços de API, as mesmas permissões, os mesmos formulários e o mesmo comportamento interno continuam exatamente como estão. O que muda é só *como se chega* a cada tela.

## Objetivo

Reduzir a barra lateral de Configurações agrupando telas relacionadas em 4 "hubs" com abas internas, no mesmo padrão visual que a tela Geral já usa hoje (`Tabs`/`TabsList`/`TabsTrigger`/`TabsContent` de `@/components/ui/tabs`). A tela Geral **não é tocada** por este trabalho.

## Regras obrigatórias (dadas pelo usuário)

- Criar somente 4 hubs: Ocorrências, Atendimento, Acesso e Integrações.
- Cada hub controla apenas navegação e renderização das telas existentes — não reescreve, duplica ou move lógica de dentro delas.
- Reutilizar o componente de Tabs já existente no projeto.
- Aba ativa persistida em `?tab=`, sobrevivendo a refresh, compartilhamento de URL e navegação back/forward.
- Todas as URLs antigas continuam funcionando via redirect para o hub + `?tab=` correspondente. Nenhuma rota antiga é removida sem cobertura de redirect.
- Permissões continuam sendo exatamente as já existentes por tela — nenhuma permissão nova é criada para as abas.
- Uma aba sem permissão não aparece e não pode ser acessada informando `?tab=` diretamente na URL — nesse caso, cai na primeira aba acessível.
- Cada hub tem uma aba padrão explicitamente definida.
- O hub não tem título próprio; o título vem do componente da tela já existente.

## Tabela de migração

Convenção de nomes: `settings-<hub>` para o nome da rota (prefixo `settings-` obrigatório porque `name: 'occurrences'` já existe, usado por `/crm/occurrences`).

### Hub: Ocorrências — `/settings/occurrences` (rota `settings-occurrences`) — aba padrão: `stages`

| Aba | Componente | Rota atual | `?tab=` | Permissão | Redirect |
|---|---|---|---|---|---|
| Etapas de ocorrência | `OccurrenceStagesView.vue` | `settings/occurrence-stages` | `stages` | `occurrences.stages` | `/settings/occurrences?tab=stages` |
| Categorias de ocorrência | `OccurrenceCategoriesView.vue` | `settings/occurrence-categories` | `categories` | `occurrences.categories` | `/settings/occurrences?tab=categories` |
| O que aconteceu | `OccurrenceWhatHappenedView.vue` | `settings/occurrence-what-happened` | `what-happened` | `occurrences.what_happened` | `/settings/occurrences?tab=what-happened` |
| Processos de ocorrência | `OccurrenceProcessesView.vue` | `settings/occurrence-processes` | `processes` | `occurrences.processes` | `/settings/occurrences?tab=processes` |
| SLA de ocorrências | `OccurrenceSLAPoliciesView.vue` | `settings/occurrence-sla-policies` | `sla` | `occurrences.sla_policies` | `/settings/occurrences?tab=sla` |
| Unidades | `UnitsView.vue` | `settings/units` | `units` | `units` | `/settings/occurrences?tab=units` |

### Hub: Atendimento — `/settings/service` (rota `settings-service`) — aba padrão: `chatbot`

| Aba | Componente | Rota atual | `?tab=` | Permissão | Redirect |
|---|---|---|---|---|---|
| Chatbot | `ChatbotSettingsView.vue` | `settings/chatbot` | `chatbot` | `settings.chatbot` | `/settings/service?tab=chatbot` |
| Respostas prontas | `CannedResponsesView.vue` | `settings/canned-responses` | `canned-responses` | `canned_responses` | `/settings/service?tab=canned-responses` |
| Etiquetas | `TagsView.vue` | `settings/tags` | `tags` | `tags` | `/settings/service?tab=tags` |
| Contatos | `ContactsView.vue` | `settings/contacts` | `contacts` | `contacts` | `/settings/service?tab=contacts` |

### Hub: Acesso — `/settings/access` (rota `settings-access`) — aba padrão: `teams`

| Aba | Componente | Rota atual | `?tab=` | Permissão | Redirect |
|---|---|---|---|---|---|
| Equipes | `TeamsView.vue` | `settings/teams` | `teams` | `teams` | `/settings/access?tab=teams` |
| Usuários | `UsersView.vue` | `settings/users` | `users` | `users` | `/settings/access?tab=users` |
| Funções | `RolesView.vue` | `settings/roles` | `roles` | `roles` | `/settings/access?tab=roles` |

### Hub: Integrações — `/settings/integrations` (rota `settings-integrations`) — aba padrão: `api-keys`

| Aba | Componente | Rota atual | `?tab=` | Permissão | Redirect |
|---|---|---|---|---|---|
| Chaves de API | `APIKeysView.vue` | `settings/api-keys` | `api-keys` | `api_keys` | `/settings/integrations?tab=api-keys` |
| Webhooks | `WebhooksView.vue` | `settings/webhooks` | `webhooks` | `webhooks` | `/settings/integrations?tab=webhooks` |
| Ações personalizadas | `CustomActionsView.vue` | `settings/custom-actions` | `custom-actions` | `custom_actions` | `/settings/integrations?tab=custom-actions` |

**Fora de escopo, intocado:** rotas de detalhe (`settings/teams/:id`, `settings/users/:id`, `settings/roles/:id`, `settings/api-keys/:id`, `settings/webhooks/:id`, `settings/canned-responses/:id`, `settings/canned-responses/new`, `settings/contacts/:id`) — mantêm path, nome e `meta.permission` exatamente como estão.

## Arquitetura

### Componente hub (4 instâncias: `OccurrenceSettingsHubView.vue`, `ServiceSettingsHubView.vue`, `AccessSettingsHubView.vue`, `IntegrationsSettingsHubView.vue`)

Cada hub:
1. Define sua lista de abas como um array `{ value, labelKey, permission, component }` local ao arquivo (dados da tabela acima).
2. Computa `visibleTabs` filtrando por `authStore.hasPermission(permission, 'read')`.
3. Lê `route.query.tab`. Se o valor não está em `visibleTabs`, usa a aba padrão do hub (se ela também não for acessível, usa a primeira de `visibleTabs`; se `visibleTabs` estiver vazio — usuário sem nenhuma permissão do grupo — mostra um estado vazio, já que a rota-guard só garante "pelo menos uma", não a aba específica pedida).
4. Ao trocar de aba, atualiza a URL com `router.replace({ query: { ...route.query, tab: novoValor } })` — `replace` (não `push`) para não empilhar uma entrada de histórico por clique de aba, mas a leitura inicial via `route.query.tab` garante que back/forward do navegador (que mudam a query via navegação real) e refresh/compartilhamento de link continuam funcionando.
5. Renderiza `<Tabs :model-value="activeTab" @update:model-value="onTabChange">` com `TabsList` (`grid grid-cols-N lg:w-auto lg:inline-flex`, padrão já usado em `MetaInsightsView.vue`) e um `TabsContent` por aba, cada um renderizando o componente existente sem props extras nem wrapper de título.
6. Sem `PageHeader` ou `<h1>` próprio do hub — o título vem de dentro do componente renderizado, como já acontece hoje.

### Router (`src/router/index.ts`)

- 4 rotas novas (uma por hub), cada uma com `meta: { anyPermission: [...todas as permissões do grupo] }` (campo novo, só para o guard reconhecer "pelo menos uma").
- As 16 rotas antigas viram `{ path: '...', redirect: to => ({ path: '/settings/<hub>', query: { tab: '<valor>' } }) }` — mantendo `path` e `name` originais (nada usa `name:` para essas rotas hoje, mas preservar por segurança).
- `router.beforeEach`: adicionar um ramo que, quando `to.meta.anyPermission` existe, aprova se `authStore.hasPermission(p, 'read')` for verdadeiro para qualquer `p` da lista; senão aplica o mesmo fallback (`getFirstAccessibleRoute`) já usado hoje. `to.meta.permission` (singular) continua funcionando exatamente como está para todas as outras rotas.
- `navigationOrder` (usado por `getFirstAccessibleRoute`): as 16 entradas dos 4 grupos dentro de `childPaths` de `/settings` são substituídas pelas 4 entradas de hub (path do hub, com a lista de permissões do grupo tratada da mesma forma — primeira permissão do grupo que o usuário tiver leva ao hub, que internamente cai na aba certa).

### `navigation.ts`

- Os 4 grupos (`nav.groupOccurrences`, `nav.groupService`, `nav.groupAccess`, `nav.groupIntegrations`) passam a ter **1 item cada**, apontando para o path do hub. `childPermissions` do item pai (`nav.settings`) continua listando todas as 16 permissões (nada muda aí — controla se o grupo aparece, não qual aba).
- Grupos não tocados (`nav.groupOrganization`, `nav.groupChannels`) continuam exatamente como estão.

## Riscos e itens de acompanhamento na implementação (não bloqueiam a spec, mas precisam ser verificados)

1. Atualizar as 6 asserções de e2e que comparam URL exata e vão quebrar (listadas na tabela de ambiguidades apresentada na revisão): `organization-switch.spec.ts:83`, `permissions.spec.ts:110,141,145`, `users.spec.ts:168`, `occurrence-permissions.spec.ts:108,136`.
2. Rodar a suíte e2e completa de `settings/*` e `crm/occurrence-permissions` depois da mudança.
3. Verificar visualmente que nenhum componente embutido fica com espaçamento estranho por perder o contexto de página inteira (nenhum dos 16 componentes será modificado, mas o layout ao redor deles muda).
4. `DashboardView.vue` mantém seus 9 links antigos (funcionam via redirect) — não mexer a menos que seja pedido depois.
5. `ChatbotSettingsView.vue` (abas internas) e `OccurrenceProcessesView.vue` (abas dentro de diálogo) continuam com suas abas próprias, agora aninhadas dentro da aba do hub — comportamento esperado, não é regressão.

## Testes

- Frontend: `npm run typecheck`, `npm run test:unit`, `npm run i18n:keys` (novas chaves de label das abas, se necessárias).
- e2e: suíte `settings/*`, `crm/occurrence-permissions.spec.ts`, e um teste novo cobrindo: aba sem permissão não aparece e não é acessível via `?tab=` direto; refresh mantém a aba; redirect de URL antiga funciona.
