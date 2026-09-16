# Central SAC — MVP de abertura manual de protocolo + widgets (Fase 0)

- **Data:** 2026-09-15
- **Status:** Aprovada — pronta para plano de implementação
- **Autor/revisão:** Ivan Coelho (product) · design colaborativo
- **Escopo:** Primeira camada da futura Central SAC & Vendas. Bot/triagem, ERP D-1, F00-F04, evidências, mapa de processos e manual são fases futuras próprias, fora deste documento.

## 1. Contexto

O módulo de Ocorrências (Fases 1-3, ver [2026-08-15](2026-08-15-crm-ocorrencias-protocolo-design.md) e [2026-09-04](2026-09-04-helpdesk-unidade-departamento-sla-design.md)) já cobre protocolo numerado, etapas, timeline, SLA por prioridade e Unidade/Departamento/Categoria — mas só no backend e por criação mínima (`ContactOccurrencesPanel.vue` só pede um título). Não existe hoje: formulário completo de abertura, dados de venda (canal/NF/data/produto), CPF/CNPJ do cliente, nem indicadores reais no Dashboard.

Este documento cobre o primeiro corte funcional: um agente identifica o cliente, preenche os dados da ocorrência e registra o protocolo — sem bot, sem ERP, sem motor de decisão. A ocorrência já criada pelas Fases 1-3 passa a ser, na prática, o protocolo de atendimento do SAC; nenhuma entidade nova de "protocolo" é criada.

## 2. Objetivos e não-objetivos

**Objetivos**
- Aba "Abrir protocolo" em `OccurrencesView.vue`, formulário para preenchimento manual do agente.
- Busca de cliente existente por telefone (`GET /api/contacts?search=`) antes de criar um novo.
- Campos novos de venda na Ocorrência: canal da venda, NF, data da compra, produto.
- CPF/CNPJ no Contact (cadastral, sem consulta a nenhum sistema externo).
- Envio automático do protocolo ao cliente reaproveitando `/send-protocol`, com feedback explícito de sucesso/falha (janela de 24h).
- Lista "Protocolos em andamento" (a `OccurrencesView.vue` atual, renomeada/expandida) mostrando os campos novos.
- 5 widgets de Ocorrências no Dashboard existente, com dados reais.

**Não-objetivos (explicitamente fora)**
- Bot/triagem conversacional, ERP D-1, F00-F04, motor de decisão, evidências/anexos, mapa de processos, manual de atendimento, dependências S01-S16, templates de WhatsApp fora da janela de 24h — todos ficam para fases próprias, já mapeadas na decomposição de subprojetos (A-F) discutida com o produto.
- Nova máquina de estados. Os status usados são os que já existem: `Stage.IsClosing` (aberto/fechado) e `SLA.Breached`/deadlines (já calculados na criação, Fase 3).
- Kanban novo, nova taxonomia de categoria.

## 3. Decisões

| Questão | Decisão | Motivo |
|---|---|---|
| Onde vive a tela | Duas abas novas em `OccurrencesView.vue`: "Abrir protocolo" (form) e "Protocolos em andamento" (lista atual, expandida). Sem módulo "Vendas" novo. | O produto pediu explicitamente para reaproveitar "o painel de ocorrência" |
| Identificação do cliente | Busca por telefone em `GET /api/contacts?search=` com debounce; se achar, usa o contato; se não achar, libera nome para criar via `POST /api/contacts` (que já deduplica por telefone normalizado) | Zero trabalho novo de backend; evita duplicar contato |
| CPF/CNPJ | Coluna nova em `Contact` (`cpf_cnpj`, nullable, indexada), sem unicidade forçada | Cadastral, pensado para a futura consulta ERP por CPF/CNPJ; unicidade ficaria fora de escopo (exigiria decidir o que fazer com duplicatas existentes) |
| Campos de venda | Colunas novas em `Occurrence`: `sale_channel`, `invoice_number`, `purchase_date`, `product_description` — todas opcionais, texto simples | Pertencem ao caso, não ao cliente; sem validação ERP nesta fase (§25 do pedido do produto) |
| Canal da venda | Enum simples e fechado no frontend (`loja_fisica`, `whatsapp`, `telefone`, `site`, `outro`), coluna `string` sem `CHECK` no banco | Fácil de trocar por catálogo configurável depois, sem migração |
| Observação interna | Não é coluna nova — vira um `OccurrenceEvent` tipo `note` criado logo após a ocorrência, reaproveitando a timeline (Fase 1) | Já existe o mecanismo; uma coluna a mais duplicaria o que a timeline já resolve |
| Título | Gerado no backend a partir de `Categoria + Produto` (`"Avaria — Piso Laminado Carvalho"`, ou `"Avaria — Atendimento SAC"` sem produto) quando o request não manda título explícito | Pedido explícito do produto: não perguntar o que dá pra derivar |
| Unidade/Departamento/Categoria | Reusa os campos que já existem em `Occurrence` (`UnitID`/`DepartmentID`/`CategoryID`) e os endpoints de Fase 3 (`/api/units`, `/api/departments`, `/api/occurrence-categories`) — sem tela de frontend própria ainda, só os `services` que faltam em `api.ts` | Fase 3 é backend-only; frontend some destes três endpoints nunca foi escrito |
| Protocolos "em aberto"/"vencidos" no widget | Contagem em **estado atual** (`closed_at IS NULL` / `resolution_deadline < now()`), **ignorando o filtro de período** do Dashboard | Um indicador operacional ("quanto está pendente agora") não pode esconder um protocolo antigo só porque o filtro está em "7 dias" — decisão registrada explicitamente, não é uma limitação silenciosa |
| Envio do protocolo | Chama `/send-protocol` sem alterá-lo. Se der 422 (fora da janela de 24h), a tela mostra "protocolo criado, mas não enviado automaticamente" — não tenta template | Regra existente do produto (§16 do pedido); não é para ser contornada agora |
| Widgets: mecanismo | `occurrences` vira uma data source a mais no motor genérico (`internal/handlers/widgets.go`), no mesmo padrão de `transfers`/`resolution_time` | Motor já existe e é genérico por design; criar um mecanismo novo duplicaria isso |
| Widgets: como aparecem | 5 `Widget` semeados automaticamente por organização (`is_default=true`, `is_shared=true`, `user_id=nil`), mesmo padrão de `ensureDefaultStages`/`ensureDefaultSLAPolicies` (seed no primeiro `GET`, idempotente por índice único) | Sem isso, o usuário teria que configurar os 5 cards manualmente pela UI genérica — o produto pediu os cards prontos |
| Painel do SAC como aba própria dentro de Ocorrências | **Não** construída nesta fase — os 5 widgets aparecem no `/dashboard` real, que já é compartilhado/editável/arrastável de graça | Duplicar a renderização dos cards (que hoje vive inline em `DashboardView.vue`, não é um componente reaproveitável) custaria uma refatoração de um arquivo grande e estável só para uma segunda vitrine dos mesmos números. Simplificação sinalizada ao produto — fácil de reverter se fizer falta |

## 4. Modelo de dados

Duas colunas novas em `contacts`, quatro em `occurrences`. Nenhuma tabela nova, nenhuma coluna removida.

### `contacts`
| Campo | Tipo | Observação |
|---|---|---|
| `cpf_cnpj` | string(20), nulo, indexado | Sem validação de dígito verificador nesta fase — texto livre normalizado (só dígitos) na gravação |

### `occurrences`
| Campo | Tipo | Observação |
|---|---|---|
| `sale_channel` | string(30), nulo | Validado contra a lista fechada no handler (não no banco) |
| `invoice_number` | string(50), nulo | |
| `purchase_date` | timestamp, nulo | Data informada pelo agente, não vinculada a nenhuma NF real |
| `product_description` | string(255), nulo | Texto livre; é o que compõe o título automático |

### `widgets` (Fase 3 do sistema de widgets, já existe — sem alteração de schema)
5 linhas semeadas por organização na primeira leitura, `data_source='occurrences'`, `is_default=true`, `is_shared=true`, `user_id=NULL`:

| Nome | metric | field | display_type | show_change |
|---|---|---|---|---|
| Protocolos em aberto | count | `open` | number | false (ver §3) |
| Protocolos vencidos | count | `breached` | number | false |
| Tempo médio de resposta | avg | `response_time` | number | true |
| Resolvidos | count | `resolved` | number | true |
| Protocolos mais recentes | count | — | table | — |

`field` aqui é um discriminador interno tratado em Go (`switch`), nunca interpolado em SQL — mesmo padrão de segurança que `transfers`/`resolution_time` já usa.

## 5. API

**Reaproveitados sem alteração de contrato:** `GET /api/contacts?search=`, `POST /api/contacts`, `POST /api/occurrences/{id}/send-protocol`, `GET /api/units`, `GET /api/departments`, `GET /api/occurrence-categories`, `GET /api/occurrence-stages`.

**Alterados (campos adicionados ao request/response, endpoint e autorização inalterados):**
```
POST /api/contacts            + cpf_cnpj (opcional)
PUT  /api/contacts/{id}       + cpf_cnpj (opcional)
POST /api/occurrences         + sale_channel, invoice_number, purchase_date, product_description, internal_note (opcional)
                               title vira opcional: se ausente, gerado de category+product_description
GET  /api/occurrences         resposta ganha os 4 campos novos + unit_name/department_name/category_name/contact_phone
GET  /api/occurrences/{id}    idem
```

**Novos:** nenhum endpoint novo. `occurrences` como data source em `GET /api/widgets/data-sources` é a única superfície nova, e é um efeito colateral de registrar a data source no mapa existente.

## 6. Frontend

| Arquivo | Mudança |
|---|---|
| `services/api.ts` | `Occurrence` ganha os 4 campos + nomes resolvidos; `CreateContactRequest`/`Contact` ganham `cpf_cnpj`; novos `unitsService`/`departmentsService`/`occurrenceCategoriesService` (não existiam) |
| `views/crm/OccurrencesView.vue` | Vira duas abas: "Protocolos em andamento" (conteúdo atual, colunas novas: telefone, unidade, SLA) e "Abrir protocolo" (form novo) |
| `components/crm/OpenProtocolForm.vue` (novo) | Form em blocos: Cliente (telefone com busca debounced → card "cliente encontrado" ou nome para criar; CPF/CNPJ) / Venda (canal, NF, data, unidade, produto) / Ocorrência (categoria, prioridade, descrição) / Observação interna opcional. Botão com loading + trava de duplo clique |
| `stores/occurrences.ts` | `createOccurrence` aceita os campos novos; novo `sendProtocolAfterCreate` que tenta o envio e devolve o resultado (sent/blocked) sem lançar em caso de 422 |
| Dashboard | Nenhuma mudança de código — os 5 widgets aparecem automaticamente assim que a org tem occurrences, via `ensureDefaultSACWidgets` no backend |

## 7. Associação com a conversa (o ponto que o produto marcou como fundamental)

Nada muda na arquitetura já decidida em Fase 1: `Occurrence.ContactID` é a âncora; `SourceTransferID` grava a conversa de origem só quando o form é aberto **a partir do chat** (`ContactOccurrencesPanel.vue`, que já existe e não é alterado). Quando o agente abre pela aba "Abrir protocolo" fora do chat, `source_transfer_id` fica vazio e `source="manual"` — exatamente o comportamento que o modelo já prevê. `ContactOccurrencesPanel.vue` já lista protocolo+etapa por contato dentro do chat (§15 do pedido do produto já está resolvido por código existente) — não precisa de mudança.

## 8. Verificação

**Go**
- `sale_channel` fora da lista fechada → 400.
- Título omitido: gerado de category+product; sem produto, cai no fallback fixo.
- `POST /api/contacts` com `cpf_cnpj` grava; contato existente encontrado por telefone não perde CPF já gravado se o campo vier vazio no request.
- Seed de widgets: idempotente sob concorrência (mesmo padrão de teste de `ensureDefaultStages`); não duplica em segunda chamada.
- `count`/`field=open`: ignora o período recebido, conta só `closed_at IS NULL`.
- `count`/`field=breached`: só conta quando `sla_resolution_deadline < now() AND closed_at IS NULL`.
- `avg`/`field=response_time`: só considera casos com `first_response_at` preenchido.
- `count`/`field=resolved`: escopado por `closed_at` no período (não por `opened_at`).
- Tabela "recentes": não retorna ocorrência soft-deletada.

**Playwright**
- Buscar telefone existente → selecionar → registrar protocolo → protocolo aparece em "Protocolos em andamento".
- Telefone novo → criar contato → registrar.
- Enviar fora da janela de 24h → mensagem de "criado mas não enviado", sem erro genérico.
- Duplo clique em "Registrar protocolo" não cria dois protocolos.

## 9. Riscos conhecidos

| Risco | Mitigação |
|---|---|
| Cliente sem histórico de WhatsApp (`last_inbound_at` nulo) nunca recebe o protocolo automaticamente | Comportamento esperado e documentado (§16 do pedido do produto); UI não esconde a falha |
| "Protocolos em aberto"/"vencidos" ignoram o filtro de período do Dashboard | Decisão deliberada (§3); widgets sem seta de tendência para não sugerir uma comparação que não existe |
| CPF/CNPJ sem validação de formato nesta fase | Aceito — é cadastro manual, não é usado para nenhuma decisão automática ainda |
