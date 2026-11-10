-- Evidência de uma conversa de homologação do Agente de IA. Somente leitura.
--
--   psql "$DATABASE_URL" -v phone=5511999990000 -f docs/homologacao/evidence.sql
--
-- :phone é o telefone do aparelho de teste, só dígitos. Imprime, em ordem: a conversa (com quem enviou
-- cada mensagem), a oportunidade, os atendimentos, as chamadas de IA e o consumo do WhatsApp com o preço
-- que a Meta informou. Anexe a saída ao roteiro de homologação.

\pset pager off
\echo '== 1. Contato'
SELECT id, phone_number, profile_name, assigned_user_id FROM contacts WHERE phone_number = :'phone' AND deleted_at IS NULL;

\echo '== 2. Conversa (quem enviou: usuário, IA ou ninguém = cliente/chatbot)'
SELECT m.created_at, m.direction,
       COALESCE(m.metadata->'sender'->>'type', CASE WHEN m.sent_by_user_id IS NOT NULL THEN 'humano' ELSE '-' END) AS remetente,
       COALESCE(m.metadata->'sender'->>'agent_name', u.full_name, '') AS nome,
       m.status, left(m.content, 90) AS texto
FROM messages m
JOIN contacts c ON c.id = m.contact_id AND c.phone_number = :'phone'
LEFT JOIN users u ON u.id = m.sent_by_user_id
WHERE m.deleted_at IS NULL
ORDER BY m.created_at;

\echo '== 3. Oportunidade no CRM'
SELECT o.opportunity_number, o.stage, o.status, o.estimated_quantity, o.unit_of_measure,
       o.assigned_user_id, o.source_transfer_id, o.interest
FROM sales_opportunities o JOIN contacts c ON c.id = o.contact_id AND c.phone_number = :'phone'
WHERE o.deleted_at IS NULL ORDER BY o.created_at;

\echo '== 4. Atendimentos (transferências)'
SELECT t.transferred_at, t.source, t.status, tm.name AS equipe, t.agent_id, t.routing_reason, left(t.notes, 120) AS notas
FROM agent_transfers t JOIN contacts c ON c.id = t.contact_id AND c.phone_number = :'phone'
LEFT JOIN teams tm ON tm.id = t.team_id
WHERE t.deleted_at IS NULL ORDER BY t.transferred_at;

\echo '== 5. Chamadas de IA (tokens, sucesso, descartes, reservas)'
SELECT l.created_at, l.feature, l.provider, l.model, l.total_tokens, l.success, l.error_kind
FROM ai_usage_logs l JOIN contacts c ON c.id = l.contact_id AND c.phone_number = :'phone'
ORDER BY l.created_at;

\echo '== 6. Consumo do WhatsApp por mensagem enviada (ator, categoria e preço informados pela Meta)'
SELECT u.sent_at, u.actor_type, u.origin_detail, u.message_type, u.billing_category, u.billing_state,
       u.meta_cost, u.meta_cost_currency, u.estimated_cost, u.ai_usage_log_id IS NOT NULL AS ligada_a_chamada_de_ia
FROM message_usage u JOIN contacts c ON c.id = u.contact_id AND c.phone_number = :'phone'
WHERE u.direction = 'outgoing' ORDER BY u.sent_at;

\echo '== 7. Consumo de tokens do mês da organização (para conferir o teto)'
SELECT date_trunc('month', l.created_at) AS mes, sum(l.total_tokens) AS tokens
FROM ai_usage_logs l
WHERE l.feature IN ('chatbot_reply', 'chatbot_flow_node', 'agent_qualification', 'ai_reservation_unreconciled')
GROUP BY 1 ORDER BY 1 DESC LIMIT 3;
