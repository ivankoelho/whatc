# Central de Vendas — Funil de Oportunidades (Entrega 1)

- **Data:** 2026-09-17
- **Status:** Aprovada — pronta para plano de implementação
- **Autor/revisão:** Ivan Coelho (product) · design colaborativo
- **Escopo:** Primeira entrega de um módulo novo, "Central de Vendas", separado de Ocorrências/SAC. Cobre o funil de oportunidades ligado ao atendimento WhatsApp e o dashboard de vendas. A conciliação automática com o ERP XProcess/X2 é a Entrega 2, tratada aqui apenas como requisito de preparação — ver §10.

## 1. Contexto

A diretoria precisa que o time de vendas tenha um funil com dashboard, ligado ao ciclo de vida do atendimento: o cliente entra em contato, escolhe "Realizar pedido" no bot, o agente assume, entende a necessidade e monta o orçamento/carrinho no ERP XProcess (X2) — hoje sem nenhum rastro estruturado no Whatomate.

O XProcess não tem API transacional própria; existe uma API Swagger que atualiza os dados do dia anterior (D-1) às 02h. Isso implica reconciliação em lote, não tempo real — e é o motivo de dividir o trabalho em duas entregas: o funil e o dashboard não podem depender de uma integração que só resolve pedidos já fechados no X2.

Este documento cobre **só a Entrega 1**: o domínio do funil, o gatilho de criação a partir do bot, a atribuição ao agente, o histórico de fase e o dashboard — com conversão marcada manualmente pelo agente. A Entrega 2 (job diário, matching com o X2, atualização automática de status) é desenhada à parte quando começar, mas o modelo de dados abaixo já nasce pronto para recebê-la sem remodelagem.

## 2. Objetivos e não-objetivos

**Objetivos (Entrega 1)**
- Entidade `SalesOpportunity`, criada automaticamente quando o cliente seleciona "Realizar pedido" no fluxo do bot.
- Três fases fixas (Potencial → Abrir Orçamento → Direcionada) com transição manual pelo agente, e dois estados finais (Convertida / Perdida), mais um quarto estado (Cancelada) reservado para a Entrega 2.
- Histórico completo de mudança de fase/status (`SalesOpportunityEvent`), nunca apagado.
- SLA de 7 dias em "Direcionada", reaproveitando o processador de SLA que já existe para Ocorrências.
- Campo novo no usuário (`xprocess_seller_code`) para a futura correspondência com o X2.
- Dashboard "Minha Operação" (carteira do agente) e visão gerencial agregada, via o motor de widgets genérico já existente.

**Não-objetivos (explicitamente fora desta entrega)**
- Qualquer chamada ao XProcess/X2. Nenhum job, nenhum cliente HTTP, nenhuma tabela de conciliação — só os campos e o desenho que tornam isso possível depois sem migração destrutiva.
- Conversão automática. Nesta entrega, "Convertida"/"Perdida" são sempre `conversion_source=manual`.
- Aba "Minha agenda" do mockup de referência (Lovable) — não fazia parte de nada que foi discutido; fica de fora até ser pedida explicitamente.
- Opção de "Orçamento" separada no bot — não existe hoje (o menu atual só tem "Realizar pedido"), e nada no desenho exige criá-la: o que hoje seria chamado de "orçamento" é a Fase 2 do funil, trabalhada pelo agente, não uma escolha adicional do cliente.
- Fases configuráveis por organização (diferente de `OccurrenceStage`) — este é um processo de negócio específico, não uma pipeline customizável.

## 3. Decisões

| Questão | Decisão | Motivo |
|---|---|---|
| Módulo novo ou extensão de Ocorrências | Módulo novo (`SalesOpportunity`), tabelas próprias | Campos e ciclo de vida diferentes o suficiente para não valer misturar — mesmo raciocínio já usado para não misturar Categoria/O-que-aconteceu de Ocorrências |
| Fases do funil | 3 fases fixas no código (`potencial`, `abrir_orcamento`, `direcionada`), não configuráveis por org | Processo de negócio específico da operação, diferente de `OccurrenceStage` (pipeline customizável por org) |
| "Direcionamento Pendente (7 dias)" do mockup | Não é uma 4ª fase — é um alerta de SLA sobre entradas em "Direcionada" há mais de 7 dias, calculado a partir de `stage_changed_at` | Evita fragmentar o funil; mesma mecânica que Ocorrências já usa para SLA |
| Mecanismo de SLA | Reaproveita o processador de SLA existente (`internal/handlers/sla_processor.go`), adicionando a checagem de "Direcionada > 7 dias" | Já existe, já roda periodicamente; criar um segundo mecanismo duplicaria infraestrutura |
| Gatilho de criação | Automático: campo novo e opcional na configuração de um botão do fluxo do chatbot (`create_opportunity: true`), no mesmo padrão que o campo `team_id` por botão já usa hoje (`execChatButtons`, `chatbot_graph_runner.go`) | Não hardcoda o texto/id do botão "Realizar pedido" em Go — qualquer botão, em qualquer fluxo, pode ser marcado como gatilho, sem precisar de deploy para reconfigurar. Automático porque o produto pediu explicitamente para evitar esquecimento do agente |
| Duplicidade no gatilho | Só cria uma nova oportunidade se o contato não tiver nenhuma **aberta** (`status=aberta`) no momento — reentrar no menu ou clicar de novo não duplica | Mesmo princípio de idempotência já usado em `hasActiveAgentTransfer` |
| Atribuição (`assigned_user_id`) | Fixado no momento da criação = `Contact.AssignedUserID` naquele instante (pode ser nulo, se o contato ainda não tem dono) | Consistente com o padrão já usado em Occurrence: `AssignedUserID` da oportunidade é independente do contato dali em diante — reatribuir o contato depois não move a oportunidade sozinha |
| Código do vendedor XProcess | Campo novo em `User` (`xprocess_seller_code`), preenchido manualmente pelo admin em Configurações → Usuários; **não** duplicado na oportunidade | A oportunidade sempre resolve o vendedor via `AssignedUserID → User`; guardar de novo é dado derivado que pode ficar desatualizado. Na Entrega 2, o `cod_vendedor` que **o X2 retornou** fica no registro de conciliação, não no cadastro do Whatomate (são coisas diferentes: um é "quem deveria ter vendido", o outro é "quem o X2 diz que vendeu") |
| Identificador da oportunidade | `opportunity_number` gerado no formato `OPP-YYYYMMDD-NNNNNN`, sequencial por organização, resetado diariamente | Mesmo mecanismo de concorrência que já protege `OccurrenceCounter` (lock + upsert), mas com granularidade diária em vez de anual — volume de vendas tende a ser maior que o de ocorrências, e a data já fica embutida no próprio código |
| Histórico de fase | Tabela nova `SalesOpportunityEvent`, nunca apagada, uma linha por mudança de fase ou status, com fase anterior, fase nova, usuário responsável, data/hora e origem (`manual`/`xprocess`/`system`) | Pedido explícito do produto — necessário para medir tempo por etapa e taxa de conversão depois. Mesmo padrão que `OccurrenceEvent` já usa para a timeline de Ocorrências |
| Exclusão física | Nunca. `SalesOpportunity` usa o soft-delete padrão (`BaseModel.DeletedAt`); não existe endpoint de exclusão permanente nesta entrega | Pedido explícito do produto: preservar histórico |
| Estados finais | Quatro, não dois: `aberta` \| `convertida` \| `perdida` \| `cancelada` | `perdida` = o processo terminou sem venda; `cancelada` = **houve** venda, mas foi revertida depois (retorno/cancelamento no X2, Entrega 2) — são resultados diferentes para a diretoria, não podem compartilhar o mesmo rótulo |
| Transição `convertida → cancelada` | Permitida (Entrega 2), sempre grava um `SalesOpportunityEvent` novo — nunca reescreve o evento de conversão original | O histórico precisa mostrar "convertida em X, cancelada em Y", não apagar que a venda existiu |
| Valor estimado vs. valor vendido | `estimated_value`/`estimated_quantity` existem nesta entrega (preenchidos pelo agente); `valor_vendido`/`quantidade_vendida`/dados do pedido X2 **não são colunas desta tabela** — entram em Entrega 2 como uma tabela de conciliação ligada por `sales_opportunity_id`, nunca sobrescrevendo o valor estimado | Preserva a pergunta "quanto havia no funil vs. quanto vendemos de fato"; evita colunas nulas por meses até a Entrega 2 existir |
| Correspondência com o pedido X2 (Entrega 2, só registrado aqui) | Por identidade: cliente (CPF/telefone) + vendedor (`xprocess_seller_code`) + janela de tempo, com níveis de confiança (forte/médio/ambíguo → fila de exceção) — não por código de referência, porque a tabela `vendas` do X2 não tem campo livre | Documentado agora para não ser esquecido quando a Entrega 2 começar; não afeta o schema da Entrega 1 |
| Dashboard | Reaproveita o motor de widgets genérico (`internal/handlers/widgets.go`), nova data source `sales_opportunities`, mesmo padrão que `occurrences` já usa | Motor já existe e é genérico por design |

## 4. Modelo de dados

Duas tabelas novas, uma coluna nova em `users`. Nenhuma alteração em tabelas existentes de Ocorrências/Contatos.

### `sales_opportunities`
| Campo | Tipo | Observação |
|---|---|---|
| `id` | uuid | |
| `organization_id` | uuid, indexado | |
| `opportunity_number` | string(20), único por org | `OPP-YYYYMMDD-NNNNNN` |
| `contact_id` | uuid, indexado | |
| `source_transfer_id` | uuid, nulo | Atendimento de origem — traceability only, mesmo papel que `Occurrence.SourceTransferID` |
| `assigned_user_id` | uuid, nulo, indexado | Carteira; fixado na criação, não segue reatribuição do contato depois |
| `stage` | string(20) | `potencial` \| `abrir_orcamento` \| `direcionada` |
| `status` | string(20) | `aberta` \| `convertida` \| `perdida` \| `cancelada` |
| `interest` | text, nulo | O que o cliente quer, texto livre |
| `estimated_value` | numeric, nulo | |
| `estimated_quantity` | int, nulo | |
| `direcionamento` | string(20), nulo | `visita` \| `whatsapp` — obrigatório antes de sair de "Abrir Orçamento" |
| `conversion_source` | string(20), nulo | `manual` \| `xprocess` (preenchido só ao converter) |
| `opened_at` | timestamp | |
| `stage_changed_at` | timestamp | Entrada na fase atual — base do cálculo de SLA |
| `converted_at` | timestamp, nulo | |
| `lost_at` | timestamp, nulo | |
| `cancelled_at` | timestamp, nulo | Preenchido só na Entrega 2 |
| `deleted_at` | timestamp, nulo | Soft-delete padrão; sem endpoint de exclusão física |

### `sales_opportunity_events`
| Campo | Tipo | Observação |
|---|---|---|
| `id` | uuid | |
| `organization_id` | uuid, indexado | |
| `sales_opportunity_id` | uuid, indexado | |
| `type` | string(20) | `opened` \| `stage_changed` \| `converted` \| `lost` \| `cancelled` |
| `from_stage` | string(20), nulo | Só em `stage_changed` |
| `to_stage` | string(20), nulo | Só em `stage_changed` |
| `source` | string(20) | `manual` \| `xprocess` \| `system` |
| `created_by_id` | uuid, nulo | Nulo = sistema/automação (ex.: SLA, ou a própria criação automática) |
| `created_at` | timestamp | |

### `users` (coluna nova)
| Campo | Tipo | Observação |
|---|---|---|
| `xprocess_seller_code` | string(20), nulo | Preenchido manualmente pelo admin em Configurações → Usuários |

## 5. Regras de transição

- **Criação (→ Potencial):** automática, ao selecionar um botão do bot marcado com `create_opportunity: true`, se o contato não tiver oportunidade aberta. Grava `SalesOpportunityEvent{type: opened, source: system}`. No editor de fluxo (`ChatNodeProperties.vue`), cada botão do node "Botões" ganha um checkbox novo e opcional ("Iniciar oportunidade de venda"), no mesmo lugar onde o `team_id` por botão já é configurado hoje — sem tela nova, só um campo a mais no painel que já existe.
- **Potencial → Abrir Orçamento:** manual, arrastar no quadro (mesma UX do quadro de Ocorrências).
- **Abrir Orçamento → Direcionada:** manual; exige `direcionamento` preenchido. Ao entrar, grava `stage_changed_at = now()` (base do SLA de 7 dias).
- **Direcionada → Convertida / Perdida:** botões explícitos ("Marcar como convertida"/"Marcar como perdida"), não drag-and-drop — ação deliberada, não um destino a mais no quadro. Grava `conversion_source=manual` e o evento correspondente.
- **Convertida → Cancelada (Entrega 2):** só a reconciliação automática faz essa transição; grava um evento novo, nunca reescreve o evento de conversão.
- Toda transição de fase ou status grava um `SalesOpportunityEvent`, sem exceção.

## 6. SLA

Ao entrar em "Direcionada", `stage_changed_at` é carimbado. O processador de SLA existente passa a checar, a cada ciclo: oportunidades com `stage=direcionada` e `stage_changed_at` há mais de 7 dias ficam marcadas como SLA vencido (mesmo campo/mecânica que `sla_breached` já usa em Ocorrências) — sem mover de fase, só um indicador visual no card/quadro e um widget dedicado ("Oportunidades com SLA vencido").

## 7. Dashboard

Duas visões, ambas via o motor de widgets genérico com `sales_opportunities` como nova data source:

**Minha Operação (por agente)** — replica as telas de referência:
- Aba "Minha carteira": cards (Atendimentos, Em potencial, Convertidos, Perdidos, Conversão, 1ª resposta média, Sem direcionamento) + quadro Kanban das 3 fases, escopados a `assigned_user_id = usuário atual`.
- Aba "Minhas vendas fechadas": histórico de conversões do próprio agente (nesta entrega, sempre `conversion_source=manual`), com valor total e volume.

**Visão gerencial (agregada):** oportunidades abertas, por etapa, valor estimado do funil, convertidas, perdidas, taxa de conversão, tempo médio até conversão (calculado a partir de `sales_opportunity_events`), oportunidades paradas / SLA vencido, vendas por agente, vendas por período.

## 8. API

**Novos endpoints:**
```
GET    /api/sales-opportunities              lista (filtros: stage, status, assigned_user_id, período)
GET    /api/sales-opportunities/{id}
PUT    /api/sales-opportunities/{id}/stage    avança fase (valida direcionamento antes de "direcionada")
POST   /api/sales-opportunities/{id}/convert  marca convertida (conversion_source=manual)
POST   /api/sales-opportunities/{id}/lose     marca perdida
GET    /api/sales-opportunities/{id}/events   histórico
```

**Alterados:**
```
PUT  /api/users/{id}     + xprocess_seller_code (opcional)
```

Data source `sales_opportunities` no motor de widgets (`GET /api/widgets/data-sources` ganha essa entrada como efeito colateral de registrá-la).

## 9. Frontend

| Arquivo | Mudança |
|---|---|
| `services/api.ts` | `salesOpportunitiesService` novo (list/get/changeStage/convert/lose/listEvents); `User`/`UpdateUserRequest` ganham `xprocess_seller_code` |
| `views/sales/SalesOperationView.vue` (novo) | "Minha Operação": abas "Minha carteira" (cards + quadro) e "Minhas vendas fechadas" (histórico + indicadores) |
| `components/sales/SalesOpportunityBoard.vue` (novo) | Quadro Kanban das 3 fases, mesmo padrão de `OccurrenceBoard.vue`/`OccurrenceCard.vue` |
| `views/sales/SalesDashboardView.vue` (novo, ou seção nova no dashboard existente) | Visão gerencial agregada — mesmo mecanismo de widgets já usado no `/dashboard` atual |
| `components/chatbot/ChatNodeProperties.vue` | Checkbox novo por botão ("Iniciar oportunidade de venda") no node "Botões" |
| `views/settings/UsersView.vue` (ou dialog equivalente) | Campo novo "Código de vendedor XProcess" no formulário de usuário |

## 10. Preparação para a Entrega 2 (não implementado agora)

Registrado aqui para não ser perdido, sem afetar o schema desta entrega:

- Job diário (após 02h) lê a API Swagger do X2, agrupa a tabela `vendas` por `cod_empresa + num_pedido`, soma os itens.
- Correspondência por cliente (CPF/telefone) + vendedor (`User.xprocess_seller_code`) + janela de tempo, com níveis de confiança: forte (CPF+vendedor) e médio (telefone+vendedor+janela, só se houver um único candidato) vinculam automaticamente; ambíguo cai numa fila de exceção para revisão manual.
- Tabela nova de conciliação (`sales_opportunity_xprocess_links` ou nome equivalente), ligada por `sales_opportunity_id`, guardando `cod_empresa`, `num_pedido`, `cod_cliente`, `cod_vendedor` (o que o X2 retornou, não o cadastro do Whatomate), `status_xprocess`, `valor_vendido`, `quantidade_vendida`, `data_venda`, `itens` — nunca sobrescrevendo `estimated_value`/`estimated_quantity`.
- Reconciliação, não importação única: o job reconsulta pedidos já vinculados (D-1, D-2, D-3, D-7) para capturar `Separado → Fechado → Cancelado`, e uma oportunidade `convertida` pode virar `cancelada` sem perder o histórico.
- Mapeamento de status X2 → Whatomate: `Separado` e `Fechado` = convertida (pagamento já confirmado em "Separado"); `Cancelado` = perdida (se nunca tinha convertido) ou cancelada (se já tinha).

## 11. Verificação

**Go**
- Seleção de botão com `create_opportunity: true` cria oportunidade em `potencial`, com `SalesOpportunityEvent{type: opened}`.
- Selecionar o mesmo botão de novo com uma oportunidade já aberta não duplica.
- `assigned_user_id` no momento da criação reflete `Contact.AssignedUserID` naquele instante (inclusive nulo).
- Reatribuir o contato depois não altera `assigned_user_id` da oportunidade já criada.
- Transição para "direcionada" sem `direcionamento` preenchido → 400.
- Toda transição de fase/status grava exatamente um `SalesOpportunityEvent` com `from_stage`/`to_stage`/`source`/`created_by_id` corretos.
- `POST .../convert` grava `conversion_source=manual`, `converted_at`, evento `converted`.
- `opportunity_number` não colide sob concorrência (mesmo teste de corrida que já existe para `OccurrenceProtocol`); reseta a sequência em um novo dia.
- SLA: oportunidade em "direcionada" há mais de 7 dias aparece marcada; ao sair da fase, deixa de aparecer, mesmo sem ter completado 7 dias antes disso.
- Exclusão física: endpoint não existe; soft-delete não remove o histórico de eventos.

**Playwright**
- Cliente seleciona "Realizar pedido" (ou o botão de teste equivalente) → oportunidade aparece em "Minha carteira" do agente atribuído.
- Arrastar pelas 3 fases no quadro; marcar convertida/perdida via botão explícito.
- Tentar avançar para "Direcionada" sem preencher direcionamento é bloqueado.

## 12. Riscos conhecidos

| Risco | Mitigação |
|---|---|
| `xprocess_seller_code` não preenchido para um agente | Sem impacto na Entrega 1 (o campo só é lido na Entrega 2); a oportunidade funciona normalmente sem ele |
| Fila de exceções da Entrega 2 pode crescer se muitos agentes não tiverem `xprocess_seller_code` cadastrado | Fora de escopo desta entrega — sinalizado para quando a Entrega 2 começar |
| Conversão manual (Entrega 1) pode divergir do que o X2 confirmar depois (Entrega 2) | Esperado e por design — é exatamente o que `conversion_source` existe para diferenciar; a Entrega 2 pode reconciliar/corrigir sem perder o histórico de que já houve uma marcação manual |
| Três fases fixas no código, não configuráveis | Aceito — é um processo de negócio específico; se a operação mudar, é uma alteração de código controlada, não uma tela de configuração |
