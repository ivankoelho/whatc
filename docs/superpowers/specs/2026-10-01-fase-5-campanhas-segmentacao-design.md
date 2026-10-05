# Fase 5: campanhas segmentadas

## O que muda
`POST /api/campaigns/{id}/recipients/from-contacts` (antes só por DDD) aceita, além de `ddds`:
`contact_type`, `unit_id` e `department_id`.

- Todos os filtros são opcionais e se combinam com AND; **pelo menos um é obrigatório**, para que um
  corpo vazio nunca signifique "toda a base". String vazia em `unit_id`/`department_id` = sem filtro.
- `contact_type` precisa ser um tipo válido (400 caso contrário). `unit_id`/`department_id` são validados
  contra a organização do chamador (outra organização = 404).
- `dry_run` conta sem gravar. Só campanhas em rascunho aceitam destinatários (regra anterior).
- Continua passando por `addCampaignRecipients`: contatos com ocorrência aberta e contatos já na campanha
  são deixados de fora, como antes. Sem alteração de schema.

## Opt-out de marketing
O envio (`worker.go`) já recusa contato com `marketing_opt_out` quando o template é MARKETING, então não havia
risco de envio indevido; porém o contato entrava no total e aparecia como "falha". Agora, na segmentação, ele é
deixado de fora no momento da inclusão, **somente para templates MARKETING** (mesma regra do worker). Importação
por CSV/manual não muda: ali só há telefone, e o worker continua sendo a barreira.

## Frontend
A aba "Por DDD" do diálogo de destinatários vira "Segmentar": DDD + tipo + unidade + departamento, com a
contagem do `dry_run` antes de confirmar. Sem permissão de unidades/departamentos, os selects ficam vazios.

## Permissões
Nenhuma nova.
