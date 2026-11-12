# Spike: indicador de digitação ("digitando…") na API oficial do WhatsApp

**Status:** somente investigação. Nenhum código de produto usa o indicador. O script
[`spike-digitacao/typing-spike.mjs`](spike-digitacao/typing-spike.mjs) roda na máquina de quem tem o
número de teste; as credenciais ficam em variáveis de ambiente e nunca vão para o arquivo de evidência.

## O que a documentação oficial confirma (consultada em 10/11/2026)

| Item | Documentado |
|---|---|
| Chamada | `POST /{versão}/{phone_id}/messages` com `{"messaging_product":"whatsapp","status":"read","message_id":"<wamid recebido>","typing_indicator":{"type":"text"}}` |
| Resposta | `{"success": true}` |
| Duração | some quando a resposta é enviada, ou sozinho após ~25 s |
| Orientação | exibir só se for responder |

**Não documentado (é isto que o spike precisa provar):** renovação, limite de chamadas, janela de 24 h,
outros valores de `type`, comportamento quando o cliente escreve mais, cobrança.

## Perguntas e como o script responde cada uma

| # | Pergunta | Passo do script | Evidência registrada |
|---|---|---|---|
| 1 | A API oficial mostra "digitando" ao cliente? | 1 | HTTP + o que você viu no aparelho |
| 2 | Quanto tempo fica? | 1 | segundos até sumir sozinho |
| 3 | Dá para renovar enquanto a IA prepara a resposta? | 2 (reenvio em 0/10/20/30 s) | se passou de 25 s, se reiniciou a contagem, erros |
| 4 | Comportamento se o cliente manda mais mensagens | 3 (você envia outra durante o indicador; opcional `--wamid2`) | se manteve, sumiu ou piscou; vale para o id novo? |
| 5 | Limites por tipo de mensagem / janela de atendimento | 5 (id inválido, `--old-wamid` com mais de 24 h) e 6 (`type: audio`) | resposta da API e efeito visível |
| 6 | Custo extra | 4 e 7 | categoria/valor que a Meta informou na resposta enviada (veja `evidence.sql`, seção 6) e se o Business Manager mostra linha extra |

A chamada de digitação não devolve id de mensagem, então ela **não pode** aparecer como linha própria no
`message_usage`. A conclusão de custo vem de comparar a resposta enviada com e sem o indicador e do painel
de cobrança da Meta (que pode demorar). Se a Meta não mostrar nada, o registro é "sem cobrança observada",
nunca "gratuito confirmado".

## Procedimento (você executa, com o número de teste)

1. Use apenas o número de teste e um aparelho seu. Nunca número de produção nem de cliente.
2. No aparelho, mande uma mensagem ao número de teste e anote o `wamid` (webhook ou banco). Para os passos
   opcionais: mande uma segunda mensagem (`--wamid2`) e tenha uma de mais de 24 h (`--old-wamid`).
3. No PowerShell, na pasta `docs/homologacao/spike-digitacao`:

```powershell
$env:WA_TOKEN = "<token do número de teste>"
$env:WA_PHONE_ID = "<phone number id do número de teste>"
node typing-spike.mjs --wamid wamid.XXXX [--wamid2 wamid.YYYY] [--old-wamid wamid.ZZZZ] --out evidence/typing-20261110.json
```

4. Siga as perguntas na tela olhando o aparelho. O arquivo é salvo a cada passo.
5. Depois do passo 7, rode `evidence.sql` para a resposta enviada no passo 4 e anexe a saída.
6. Envie o JSON de evidência. O token é redigido; confira com `grep -c "$env:WA_TOKEN"` antes de enviar.

Ensaio sem Meta: `WA_BASE_URL=http://127.0.0.1:<porta>` aponta para um simulador local (já verificado:
22 passos, token ausente do arquivo).

## Critério de decisão (para a fase de espera inteligente)

- **Construir com renovação** se o passo 2 provar que reenviar mantém o indicador além de 25 s.
- **Construir sem renovação** (mostrar uma vez, 25 s) se a renovação falhar; a IA que demora mais que isso
  fica sem indicador, o que é aceitável.
- **Não construir** se a API recusar fora da janela ou se aparecer cobrança extra.
- Em qualquer caso o indicador só é enviado quando a IA já decidiu responder (regra da Meta) e nunca
  depois de um humano assumir.
