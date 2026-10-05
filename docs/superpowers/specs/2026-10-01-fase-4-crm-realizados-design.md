# Fase 4: CRM, unidade de medida e valor/quantidade realizados

## Semântica
- `estimated_value` / `estimated_quantity` / `unit_of_measure` = **expectativa comercial** (editáveis enquanto a
  oportunidade está `aberta`).
- `realized_value` / `realized_quantity` = **o que foi efetivamente vendido** (preenchidos na conversão ou
  depois, com a oportunidade `convertida`). Nunca misturam com os estimados.
- **X2 é a fonte oficial do valor realizado quando há pedido reconciliado.** `realized_quantity` é manual: o
  endpoint atual do X2 devolve só total, status, empresa e vendedor (sem itens, quantidade ou unidade).

## Schema (`sales_opportunities`, todas nullable, sem DROP)
| Coluna | Tipo | Observação |
|---|---|---|
| `estimated_quantity` | `numeric(14,3)` | era `bigint`; `ALTER COLUMN TYPE` feito pelo AutoMigrate. Valores preservados (50 → 50.000). Nenhum índice/constraint dependia do tipo. |
| `unit_of_measure` | `varchar(10)` | CHECK `chk_sales_opp_unit_of_measure` gerado de `models.SalesUnitsOfMeasure` (DROP IF EXISTS + ADD a cada start, então ampliar a lista é uma linha) |
| `realized_value` | `numeric` | |
| `realized_quantity` | `numeric(14,3)` | |

Unidades: `UN`, `M`, `M2`, `KG`, `PCT`, `CX` (lista fechada, sem tabela de cadastro). A migração foi exercitada
numa cópia de um banco existente, duas vezes (idempotente). O banco arredonda para 3 casas, por isso a API
**rejeita** (400) mais de 3 decimais, valores negativos ou acima de 99.999.999.999,999.

## API
- `POST /sales-opportunities`: aceita `estimated_quantity` e `unit_of_measure` (além de interest/value). Não aceita
  `realized_*`: nada foi realizado quando a oportunidade abre. Como interest/value, só valem para uma oportunidade
  *nova* (um retrigger de uma aberta os ignora).
- `PUT /sales-opportunities/{id}/details`: aberta → interest, estimated_*, unit (`""` limpa a unidade);
  convertida → só `realized_*`; qualquer outro caso ou mistura → 400.
- `POST /sales-opportunities/{id}/convert`: corpo opcional com `realized_value` / `realized_quantity`; sem corpo
  funciona como antes e deixa ambos nulos.
- Reconciliação X2 (`reconcileXProcessLink`): em SEPARACAO/SEPARADO/FECHADO, com total > 0, grava o total do pedido em
  `realized_value` da oportunidade **convertida** (substitui um valor manual; acompanha o pedido até ele fechar).
  CANCELADO, total zero, oportunidade não convertida: nada é escrito. Nunca altera `estimated_*` nem `realized_quantity`.

## Permissões
Só `sales_opportunities:read|write|view_all` (as mesmas regras de dono/view_all dos demais endpoints).

## Frontend
Card do funil e tabela da carteira mostram quantidade/unidade e realizado; um diálogo "Editar detalhes" edita
estimados (aberta) ou realizados (convertida); o diálogo de criação ganha quantidade e unidade. A conversão no
card continua em um clique; o realizado é informado depois, pelo diálogo. Sem widget de realizado nesta fase.

## Preparado para o futuro
Quando o X2 expuser itens do pedido, basta preencher `realized_quantity`/`unit_of_measure` em
`setRealizedValueFromXProcess` (hoje só o valor).
