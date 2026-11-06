# Segurança do webhook da Meta

## Auditoria das rotas

| Rota | Tipo | Autenticação |
|---|---|---|
| `GET /api/webhook` | verificação de assinatura de inscrição da Meta | `hub.verify_token` (global `whatsapp.webhook_verify_token` ou o token da conta), comparação em tempo constante; token vazio nunca vale |
| `POST /api/webhook` | **único** endpoint de entrada da Meta (mensagens, estados, ligações, templates, preferências) | `X-Hub-Signature-256` obrigatório, ver abaixo |
| `/api/webhooks*` | CRUD dos webhooks de **saída** configurados pelos usuários | sessão/JWT + permissões `webhooks:*` (não recebem dados externos) |
| `/api/auth/sso*` | callback de SSO | `state` próprio do fluxo OAuth |

Não há outros provedores externos chamando a aplicação. Se houver no futuro, cada um terá o seu mecanismo; a assinatura da Meta **não** se aplica a eles.

## Regra do `POST /api/webhook`

HMAC-SHA256 do **corpo bruto** com o App Secret, comparado em tempo constante (`hmac.Equal`). Falha fechada:

- cabeçalho ausente, sem `sha256=`, hexadecimal inválido, truncado ou de outro corpo → **403**;
- a assinatura precisa verificar sob o segredo de **cada** conta citada no corpo (`phone_number_id`, ou o WABA id nas atualizações de template). Verificar só uma permitiria que o dono de uma conta forjasse eventos de outra organização;
- segredo da conta, senão `whatsapp.app_secret` da instância; se nenhum existir → 403 (nunca "aceita porque não há segredo");
- corpo sem nenhuma alteração (`entry` vazio) não tem estado a proteger e responde 200; alteração sem conta identificável → 403;
- não existe chave de configuração para desligar a verificação. Testes locais assinam o corpo (`test/tools/mock-meta.js`, rota `/__webhook`);
- o log traz só o motivo e o tamanho do corpo; nunca segredo, token ou conteúdo.

Duplicados: a mesma entrega repetida é inofensiva (estados só avançam). A Meta não assina data/hora, então não há proteção contra reenvio de um corpo legítimo; a idempotência é o que o torna seguro.

## Configuração e rotação do segredo

1. Onde fica: por conta (`app_secret` em *Configurações → Contas*, cifrado) ou, para uma única app Meta, `whatsapp.app_secret` / `WHATOMATE_WHATSAPP__APP_SECRET`.
2. Contas sem segredo próprio **passam a ser rejeitadas** se a instância também não tiver um: configure antes de atualizar.
3. Rotação: gere o novo App Secret no painel da Meta e atualize **imediatamente** a conta (ou a variável da instância e reinicie). Entre os dois passos os webhooks da Meta falham com 403 e são reenviados pela Meta com recuo; faça fora do horário de pico. Depois confirme um evento de teste.
4. Nunca coloque o segredo em log, ticket ou chat.
