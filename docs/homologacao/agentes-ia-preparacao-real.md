# Preparação da homologação real do Agente de IA

Complementa o [roteiro](agentes-ia-roteiro.md). Escopo autorizado: **organização isolada, um único número de
teste autorizado**. Não usar a organização de produção nem números de clientes.
Continuam proibidos sem autorização expressa: push, merge, deploy, ativação em produção.

## O que só você pode fornecer (eu não manuseio estas credenciais)

| Item | Para quê |
|---|---|
| Número de teste da Meta (phone id, token, app secret, verify token) | envio/recebimento reais |
| URL pública HTTPS apontando para o servidor (túnel ou domínio de homologação) | webhook da Meta |
| Chave de IA da organização de teste | o agente responder de verdade |
| Aparelho(s) autorizado(s): o do cliente de teste e, para o cenário "número não autorizado", um segundo | cenários 1–10 |

## Montagem

1. **Binário limpo da branch**, sem ganchos de simulador:
   ```bash
   git checkout feature/agente-ia-base
   go build -o whatomate.exe ./cmd/whatomate
   ```
2. **Banco novo e vazio** (nunca o de produção nem o de desenvolvimento): crie um banco e um Redis só da
   homologação; `config.toml` com `database.url` e `redis.url` deles. Rode `whatomate.exe server -migrate`.
3. **Organização isolada**: crie uma organização nova pela tela de cadastro. Nela:
   - uma conta WhatsApp (o número de teste), com **equipe padrão** de uma **unidade** (equipe ativa);
   - uma segunda equipe sem unidade, usada como "fila geral" no cenário 3.6;
   - usuários: um admin, um vendedor da equipe da unidade;
   - Configurações → Chatbot: habilitar; IA habilitada com sua chave; **limite mensal de tokens** baixo (ex.: 3000);
   - Agentes de IA: um agente, **habilitado**, **marcado como padrão**, nome de exibição (padrão "Assistente
     Virtual"), e **somente a conta do número de teste** em "números permitidos".
4. **Webhook**: configure a URL pública e o verify token na Meta.
5. Conferir antes de começar: o painel de prontidão do agente sem pendências; mensagem de um número que não
   é o de teste não gera resposta (cenário "não autorizado").

## Conferência de identificação (novo)

- No WhatsApp do cliente, cada mensagem da IA começa com `*Assistente Virtual:*` (ou o nome configurado).
- Mensagens do vendedor humano **não** têm essa assinatura.
- No chat do atendente, mensagens da IA mostram o selo "IA"; as humanas, o nome do usuário.
- `evidence.sql`: coluna `remetente` = `ai_agent` nas da IA, `humano` nas do vendedor; na seção 6, `actor_type = ai`
  para respostas e para a mensagem de transferência.

## Execução e evidências

Siga os cenários do roteiro. Ao fim de cada um:

```bash
psql "$DATABASE_URL" -v phone=55XXXXXXXXXXX -f docs/homologacao/evidence.sql > evidencias/cenario-N.txt
```

Guarde também capturas do chat e do CRM. Anexe ao relatório o resultado (passou/falhou) por cenário.

## Condições de parada (interrompa e avise)

- A IA responder a um número fora da lista, ou depois de um humano assumir.
- Mensagem da IA sem assinatura, ou mensagem humana com assinatura de IA.
- Preço, estoque ou prazo afirmados como certos pela IA.
- Oportunidade duplicada ou transferência sem equipe/fila.
- Consumo acima do teto configurado.
- Qualquer sinal de que a organização de teste compartilha dados com a de produção.

## Reversão

- Desabilitar o agente na tela (a IA para; o chatbot normal segue).
- Descartar o banco e o Redis da homologação; apagar o webhook na Meta.
- A migração do nome de exibição é aditiva; reversão: `ALTER TABLE ai_agents DROP COLUMN display_name;`.
- Nada foi enviado a `origin`; a branch é local.
