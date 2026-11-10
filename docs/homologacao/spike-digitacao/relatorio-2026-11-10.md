# Relatório do spike do indicador de digitação — 10/11/2026

Número de teste da Meta (`Test Number`, Cloud API, v24.0). Duas rodadas reais. Nada simulado entra nas
conclusões abaixo; o ensaio contra simulador local serviu só para validar o script. Nenhum código de produto
foi alterado. Números de telefone, ids de mensagem e tokens foram omitidos.

## Preparação (real)

- O webhook do app apontava para o ambiente de produção, então o do número de teste foi sobrescrito (somente
  esse número) para um receptor descartável fora do repositório. A Meta validou o desafio. Ao fim a
  sobrescrita foi removida; o número voltou a mostrar apenas o webhook do app.
- O receptor gravava só id, horário, tipo, categoria de cobrança e últimos 4 dígitos.

## Resultados da API (reais)

| Item | Resultado |
|---|---|
| Chamada de digitação com `message_id` recebido | HTTP 200 `{"success":true}`, 0,5–1,4 s |
| Renovação a cada 10 s (4 chamadas em 30 s) | todas HTTP 200, sem limite de frequência observado |
| `typing_indicator.type = "audio"` | HTTP 400: o schema só aceita `[text]` |
| `message_id` inválido | HTTP 400 `#131009` |
| Resposta de texto dentro da janela | HTTP 200; status enviada/entregue/lida com `category: service`, `billable: false`, `pricing_model: PMP` |
| Resposta a número fora da lista de destinatários (só no número de teste) | HTTP 400 `#131030` |

## Observação no aparelho (relatada pelo dono do aparelho, sem cronometragem em segundos)

| Pergunta | Observado |
|---|---|
| O indicador aparece ao cliente? | Sim |
| Renovando, ele se mantém? | Sim (o relato não mede os segundos) |
| Cliente envia nova mensagem enquanto aparece | Sumiu e voltou a aparecer (voltou na renovação seguinte) |
| A resposta o encerra? | Sim, sumiu na chegada |

## Não provado

- Duração exata sem renovação (documentação: ~25 s; o relato não mediu).
- Janela de 24 h (não houve mensagem antiga para testar).
- Cobrança das próprias chamadas de digitação: não geram id e não aparecem no consumo; falta conferir o
  painel de cobrança da Meta após o ciclo.

## Parecer

Viável para a fase "espera inteligente", com estas regras de projeto:
1. Mostrar o indicador só quando a IA já decidiu responder, nunca depois de humano assumir.
2. Renovar em intervalo curto (testado a 10 s) enquanto a IA prepara a resposta.
3. Reenviar o indicador a cada nova mensagem do cliente, pois a chegada dela o apaga.
4. Somente `text`. Falha da chamada nunca bloqueia nem atrasa a resposta (melhor esforço).
5. Antes de liberar para clientes: conferir o painel de cobrança e testar a janela de 24 h.
