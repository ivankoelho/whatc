# Etapa 6 — Central de Serviços: chat interno, tela unificada e segurança do webhook

Branch `feature/team-role-profiles`. "Protocolos" continua sendo o nome técnico; "Central de Serviços" é o nome da área operacional.

## 6.0 Webhook da Meta
Detalhes em `2026-10-25-webhook-seguranca.md`. Resumo: toda chamada `POST` ao webhook exige `X-Hub-Signature-256` (HMAC-SHA256 sobre o corpo bruto), verificada com o segredo de **cada** conta referenciada (segredo da conta, senão `whatsapp.app_secret`); sem segredo, sem assinatura ou com assinatura errada → 403. Comparação em tempo constante; não há flag de bypass. Webhooks de outros provedores não são afetados (a regra vale só na rota Meta).

## 6.1 Chat interno do chamado
- Reutiliza o chat de equipe: canal `kind = occurrence`, um por chamado (índice único `idx_chat_channel_occurrence`).
- Acesso derivado do chamado (`occurrenceChatAccess`): ler exige acesso ao chamado + `occurrences:read`; postar exige capacidade `comment` + `occurrences:write`. Eventos em tempo real passam pelo autorizador do chamado.
- O chat interno nunca chega ao WhatsApp: endpoints distintos, tabelas distintas (`chat_messages` × `messages`).
- Notas antigas permanecem no histórico (o formulário de nota livre foi removido da tela; notas de cupom/NF continuam).

## 6.2 Tela unificada
Duas colunas: dados do chamado, pedido (referência, sem ERP), documentos e histórico à esquerda; cartão WhatsApp (canal do chamado), comunicação automática, discussão interna e participantes à direita. Resposta antiga só aparece quando não há conta WhatsApp do chamado.

## 6.3 Kanban
Os 4 estados gerais foram preservados; o cartão abre a mesma tela; nenhum endpoint novo.

## 6.4 Consentimento / elegibilidade
Implementado: chave mestra `whatsapp.automation_sending_enabled` (padrão **false**); envio automático só para cliente que escreveu nos últimos 90 dias; registro de `consent_basis`, `consent_evidence`, `purpose`; só templates UTILITY aprovados; texto livre só com janela de 24h aberta na conta do chamado.

Bloqueios antes de envio real (decisão de negócio/jurídica, não implementados):
1. origem formal da autorização (opt-in) para além do contato iniciado pelo cliente;
2. finalidade documentada por template;
3. cadastro de preferência "não contatar" consultado antes de cada envio;
4. política de retenção e auditoria das evidências;
5. homologação dos templates na Meta.

## 6.5 Testes
Go: `occurrence_chat_test.go` (IDOR entre organizações/lojas, participante sem capacidade, assinatura de WS, anexo, interno→WhatsApp, concorrência), `occurrence_whatsapp_test.go`, `occurrence_eligibility_test.go`, `webhook_security_test.go`. Frontend: `OccurrenceWhatsAppCard.spec.ts`. Navegador: ver relatório final.

## Limitações
- indicador de "digitando" reflete o próprio usuário em algumas telas;
- sem menção (@) no chat do chamado;
- participantes precisam das permissões `team_chat:*` para usar o chat;
- pedido é só referência: não há integração de ERP;
- a documentação pública `webhooks.mdx` ainda pode citar assinatura opcional.

## Revisão final de segurança e interface

**Conteúdo da conversa de WhatsApp do chamado (menor privilégio).** Supervisionar um chamado (inclusive por unidade) não abre a conversa. A leitura exige `chat:read`; o envio exige também `chat:write`; e é preciso ser o responsável, membro da equipe do chamado ou ter a permissão explícita `conversations:view_all`. O papel "Supervisor de SAC" mantém acesso por ter `conversations:view_all`; se a decisão de negócio for restringi-lo, retirar essa permissão do papel. Testes: `TestCaseWhatsApp_NeedsExplicitChatPermission`.

**Ambiente de desenvolvimento (somente banco `whatomate_valida`, local).** Para validar no navegador, as senhas das contas de teste `sac`, `suploja`, `supsac` e `logistica2` (domínio `perfil.local`) foram redefinidas para um valor de teste via API de administração. Nada disso está em commit, seed ou relatório; contas existem só no banco local. Também foi marcada manualmente como `uncertain` a notificação do chamado 2026-000220 para exibir o estado "resultado incerto" na tela. Não afetam produção nem homologação.

**Mensagem de "não encontrado".** A tela do chamado passa a usar `occurrences.notFoundHint` (pt-BR e en) no lugar do texto padrão em inglês do layout.

**Layout em duas colunas.** Medido no navegador: a 1366 px cada coluna tem 608 px; a 1920 px o conteúdo é limitado a 1600 px e cada coluna tem 788 px; bordas esquerdas das duas colunas coincidem nas linhas de Solicitação, WhatsApp e Discussão. Limitação conhecida: com a discussão interna vazia, o bloco acompanha a altura da coluna do WhatsApp.

## Supervisão de chamados × leitura de conversas (decisão final)

Supervisionar chamados e ler conversas de WhatsApp são poderes distintos.

- Nova permissão `occurrences:view_all`: enxerga e supervisiona **todos os chamados** (lista, Kanban, indicadores, discussão interna, auditoria de escopo, etapas de equipe). Não abre nenhuma conversa de cliente. `conversations:view_all` continua implicando a supervisão de chamados (ninguém perde o que tinha).
- Conteúdo da conversa de WhatsApp do chamado (`GET/POST /api/occurrences/{id}/whatsapp`): leitura exige `chat:read`; envio exige também `chat:write`; e uma destas autorizações explícitas: ser o responsável, membro da equipe do chamado, membro da equipe dona da conta WhatsApp (`default_team_id`, o mesmo mecanismo que as conversas já usam) ou ter `conversations:view_all`.
- Perfil padrão "Supervisor de SAC": `conversations:view_all` saiu; entraram `conversations:view_team` e `occurrences:view_all`. A permissão ampla continua podendo ser concedida explicitamente a quem for autorizado.

### Matriz de permissões (perfis padrão)

| Perfil | Chamados | Conversa WhatsApp do chamado |
|---|---|---|
| SAC | próprios e participações | só o resumo do envio automático |
| Logística | equipe/unidade (membro da equipe) | lê e responde (equipe do chamado, `chat:*`) |
| Supervisor de Loja | chamados da unidade (`view_team`) | lê e responde se for membro da equipe do chamado ou da conta |
| Supervisor de SAC | todos os chamados (`occurrences:view_all`) | só contas/equipes a que pertence; sem `view_all` não lê Logística |
| Administrador | tudo | `conversations:view_all` explícito |

### Migração e implantação

- `BackfillOccurrenceViewAll`: concede `occurrences:view_all` a todo papel que já tem `conversations:view_all` (aditivo; roda uma vez por organização — se algum papel da organização já tem a permissão, não reaplica, para não desfazer revogações).
- `ReconcileSupervisorSACPreset`: remove `conversations:view_all` de um papel "Supervisor de SAC" **somente se ele ainda for exatamente o preset anterior** (nunca personalizado), trocando por `conversations:view_team`. Papel editado por alguém é mantido e registrado em log com a diferença. Toda alteração é registrada em log (nível warn). Não toca em concessões individuais nem em outros papéis.
- Atenção no deploy: as permissões de usuário ficam em cache no Redis (`permissions:user:*`, TTL de horas). Após a migração, limpe essas chaves ou aguarde o TTL para a mudança valer de imediato.
