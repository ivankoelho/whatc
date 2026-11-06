# Chamados — Etapa 5: comunicação automática com o cliente (WhatsApp)

Complementa a [etapa 4](2026-10-25-chamados-etapa4-fluxo-sac-logistica.md). Tudo foi validado com um provedor **simulado**; nenhuma mensagem real foi enviada.

## 1. Auditoria (o que já existia e foi reaproveitado)

| Pergunta | Como é hoje |
|---|---|
| Contas WhatsApp | Tabela `whatsapp_accounts`, nome único por organização; token e segredos cifrados. Envio manual escolhe a conta: explícita > template > contato > padrão (`resolveWhatsAppAccount`). |
| Número de origem | `account.PhoneID` entra na URL do envio; a mensagem grava o **nome** da conta (`messages.whats_app_account`). |
| Templates | Tabela `templates` sincronizada com a Meta (`status` PENDING/APPROVED/…, `category`, `language`, `body_content`). |
| Envio | `SendOutgoingMessage` é o único remetente: cria a linha em `messages`, chama a API, grava `whats_app_message_id`, alimenta o ledger de consumo e os webhooks internos. Templates ignoram a janela de 24 h. |
| Filas/retentativas | Campanhas usam stream Redis; envio avulso não tinha retentativa. O worker novo não depende do Redis: usa a própria tabela como fila. |
| Webhooks de estado | `POST /api/webhook` → `updateMessageStatus` (progressão monotônica sent→delivered→read, failed vence). |
| Consentimento | Só existe `MarketingOptOut`, que vale para marketing. Por isso a automação aceita **apenas templates UTILITY**. |
| Infra da etapa 4 | `occurrence_notifications` (intenção, chave idempotente) e `occurrence_channel_configs`, estendidas aqui. |

Conflitos com OTP/segurança: **nenhum arquivo de OTP, autenticação, campanhas ou worker foi alterado.** Os dois pontos compartilhados são aditivos: `messages.go` (campos opcionais `ExtraMetadata` e `OnResult`, nulos por padrão) e `webhook.go` (uma chamada após localizar a mensagem). `pkg/whatsapp` passou a devolver erros tipados (`APIError`, `TransportError`) **com o mesmo texto de antes**.

Observação de segurança (não alterada): `WebhookHandler` só verifica a assinatura quando o cabeçalho `X-Hub-Signature-256` está presente; sem ele, o corpo é aceito. Vale endurecer fora desta entrega.

## 2. Configuração (por organização, unidade e equipe)

`occurrence_channel_configs` (tela *Configurações → Protocolos → Comunicação com o cliente*, API `/api/occurrence-channel-config`):

- escopo: **equipe > unidade > organização**; a linha mais específica vale mesmo inativa (desligar numa unidade não "cai" na da organização);
- evento (`forwarded`), conta, template + idioma, template alternativo (sem número de pedido), ativa/inativa, tentativas, intervalo, comportamento em falha (`manual_retry` ou `cancel`);
- ao salvar ativa: a conta precisa ser **da organização** e o template precisa existir nela, estar **APROVADO** e ser **UTILITY**. Conta de outra organização → 400; nunca se usa outra conta no lugar;
- permissão: `accounts:write` (administração do WhatsApp). Agentes e supervisores comuns recebem 403; `accounts:read` pode listar.
- o número 3040-9602 **não está no código**: é só uma conta cadastrada e escolhida na configuração.

## 3. Intenção de comunicação

Ao encaminhar o chamado, **na mesma transação** (chamado + evento `forwarded` + participante + intenção) grava-se `occurrence_notifications` com chave `occ:<id>:created` (única: evento repetido não cria outra) e um **snapshot** de conta, template, idioma e política. Mudar a configuração depois não altera o que já foi enfileirado. Nada chama a Meta dentro dessa transação.

Estados: `prepared` (automação inativa), `pending`, `processing`, `accepted` (aceita pelo provedor), `delivered`, `read`, `failed`, `uncertain`, `cancelled`, `skipped` (com `reason`).

## 4. Worker

`StartOccurrenceNotifier` (a cada 5 s; vários processos podem rodar):

1. **Claim atômico**: `UPDATE … SET status='processing' WHERE id=? AND status='pending'`; só um worker vence.
2. **Revalida no momento do envio**: automação ainda ativa, conta na organização, nome do cliente, template existente/aprovado/UTILITY, parâmetros (sem valor inventado: sem pedido → template alternativo; sem alternativo → `skipped missing_order_number`).
3. Envia por `SendOutgoingMessage` (síncrono) e recebe o resultado por `OnResult`.
4. Classifica: recusa do provedor (4xx) → `failed`; 429 → nova tentativa com intervalo exponencial; **5xx, queda de conexão, timeout** e **worker que morreu no meio** → `uncertain`.
5. Grava a tentativa (`occurrence_notification_attempts`: conta, template, horários, resultado, id do provedor, erro, quem pediu) e um evento `customer_notification` na linha do tempo.

**Garantia, sem exagero:** nenhuma promessa de "exatamente uma vez" — a Meta não oferece chave de idempotência para envio. O sistema é *no máximo uma vez* para resultados ambíguos e *ao menos uma vez* para recusas definitivas. Resultado incerto **nunca** é reenviado sozinho; um **supervisor** precisa pedir e confirmar explicitamente o risco de duplicidade (`confirm_duplicate_risk`). Um webhook do provedor para a mensagem também resolve o `uncertain` (é evidência de aceitação).

## 5. Webhooks

`updateMessageStatus` passa a avançar também a notificação, pela `occurrence_notification_id` guardada nos metadados da mensagem (encontra a notificação mesmo que o webhook chegue antes de o id ser salvo). Regras: só avança (accepted → delivered → read); duplicado ou fora de ordem não muda nada nem gera evento; `failed` do provedor vence até `delivered`.

## 6. Interface

- **Chamado**: cartão *Comunicação com o cliente* — conta, template, estado, horários, id da mensagem, motivo da falha, histórico de tentativas; botões *Tentar de novo*/*Reenviar (pode duplicar)*/*Cancelar* só para quem pode (responsável ou supervisor); atualiza sozinho enquanto há estados ativos.
- **Configurações**: lista com escopo, conta, template, interruptor ativa/inativa, última execução e falhas recentes (7 dias); criar/editar/excluir com `accounts:write`.
- O estado do chamado **não muda** por causa da mensagem.

## 7. Contatos compartilhados

`GET /api/contacts/find?q=` (`contacts:read`): por telefone (≥ 8 dígitos) ou nome (≥ 4 letras), no máximo 10 resultados, 30 consultas/min por usuário, sempre na organização da sessão. Devolve só `id`, telefone, nome e `can_open_conversation`. **Não** devolve CPF/CNPJ, responsável, equipe nem mensagens, e não abre a conversa. O formulário de protocolo usa o localizador; criar chamado continua exigindo `occurrences:write` e, sem acesso à conversa, o encaminhamento a uma equipe.

## 8. Limitações conhecidas

- O worker roda em cada instância do servidor; vale um único consumidor em produção até haver monitoramento do `uncertain`.
- `uncertain` por 5xx/timeout só se resolve por webhook com o mesmo id (não conhecido se a requisição nunca devolveu resposta) ou por decisão humana.
- Sem verificação de consentimento próprio além de exigir UTILITY; opt-in de utilidade deve ser garantido pelo processo comercial.
- Reenvio/novos eventos (ex.: "saiu para entrega", "pronto para retirada") reutilizam a estrutura (`event`, `kind`), mas ainda não têm gatilhos.
- O localizador não substitui a lista de contatos: mostra pouco por desenho.

## 9. Como testar localmente sem enviar nada

```bash
node test/tools/mock-meta.js                       # provedor simulado em :9100
WHATOMATE_WHATSAPP__BASE_URL=http://localhost:9100 go run ./cmd/whatomate server -config config.toml -migrate
curl "localhost:9100/__mode?m=ok"                  # ok | 400 | 429 | 500 | drop
curl localhost:9100/__reqs                         # o que o servidor tentou enviar
```

Status de entrega/leitura: `POST /api/webhook` com `statuses[]` (sem assinatura, como o endpoint já aceita sem o cabeçalho) e o `id` devolvido pelo simulado. Testes Go: use base e Redis próprios (`TEST_DATABASE_URL=…/whatomate_test_<sessao>`, `TEST_REDIS_URL=redis://…/<n>`), nunca os compartilhados com outra sessão.
