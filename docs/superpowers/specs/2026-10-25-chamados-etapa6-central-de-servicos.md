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
