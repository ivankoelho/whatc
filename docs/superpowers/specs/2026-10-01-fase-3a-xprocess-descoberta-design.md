# Fase 3A: descoberta assíncrona de pedidos X2, frete e correção de data

## Problema
O X2 é D-1: o pedido só chega à API na manhã seguinte. Um agente pode converter a oportunidade no dia da venda, e
o pedido só existe no X2 depois, sem que ninguém tenha digitado o número. O job atual (`reconcileXProcessLink`)
só consulta `POST /api/pedido {numero_pedido, documento}`, ou seja, precisa do número. A 3A acrescenta uma
**descoberta por documento** e as travas para nunca atribuir um pedido por engano.

## O que a API do X2 oferece (amostras reais de 2026-10-01)
- `/api/pedido` e `/api/vendas`: uma linha por item, com `cod_empresa`, `num_pedido`, `cod_vendedor`, `status`,
  `data_venda` (**sempre `T00:00:00`**), `total` (= subtotal − desconto), `vl_frete` (por item; a soma é o frete do pedido).
- `/api/vendas` só aceita `cod_cliente`, `cod_vendedor` e `limit` (máx. 1000): **sem paginação e sem ordem garantida**.
  Cliente antigo (24 pedidos/49 linhas) devolveu o histórico completo desde 2023, em ~1,5 s; inclui `CANCELADO`.
- Primeira chamada fria chegou a ~8 s.

## Descoberta (`xprocess_discovery.go`, dentro de `RunXProcessReconciliation`, depois da varredura dos vínculos)

**Interruptor: `xprocess.discovery_enabled`, DESLIGADO por padrão.** Chave ausente, seção vazia ou valor vazio = desligado;
valor inválido faz a configuração falhar ao carregar (nunca liga). Também por ambiente:
`WHATOMATE_XPROCESS__DISCOVERY_ENABLED=true`. Desligado, só a **descoberta** deixa de rodar: a reconciliação dos vínculos
existentes, o vínculo manual e a lista de candidatos continuam funcionando. Assim, código implantado não significa
funcionalidade ativa: liga-se depois de validar no ambiente (o log de cada varredura informa `discovery_enabled`).
Alvo: oportunidade `convertida`, **sem nenhum vínculo X2**, convertida há **no máximo 7 dias**, com documento
conhecido (`xprocess_documento` ou `Contact.CPFCNPJ`, ambos validados por `contactutil.NormalizeDocumento`).

`documento → GET /api/clientes → cod_cliente → GET /api/vendas?limit=1000 → agrupar por (cod_empresa, num_pedido)`
(uma vez por documento por rodada). Regras, **todas** precisam valer:

| Regra | Detalhe |
|---|---|
| Status | só `SEPARACAO`, `SEPARADO`, `FECHADO`; `CANCELADO` nunca abre vínculo novo |
| Data | `data_venda.Date >= data de abertura da oportunidade` (comparação por **data**, no fuso da Bahia; a venda do mesmo dia conta) |
| Pedido livre | nenhum vínculo (qualquer oportunidade) já rastreia o pedido: mesmo `(cod_empresa, num_pedido)` ou mesmo `num_pedido` + documento |
| Vendedor | só veta: se o dono da oportunidade tem `XProcessSellerCode` **e** o pedido traz `cod_vendedor` e são diferentes → fora. Sem código de um dos lados → não bloqueia |
| Unicidade | **exatamente 1** pedido candidato **e exatamente 1** oportunidade elegível. 0 ou mais de 1, de qualquer lado → nada é vinculado (o pedido segue como candidato manual) |
| Conjunto | todas as oportunidades abertas, e as convertidas dentro da janela, do mesmo documento, sem vínculo e de **qualquer contato** (CPF repetido é permitido desde a Fase 2) |
| Lista cheia | `/api/vendas` com exatamente 1000 linhas pode estar truncada: nunca associa automaticamente |
| Aberta | oportunidade aberta nunca é vinculada nem convertida pela descoberta |
| Imutabilidade | a descoberta nunca substitui, desfaz ou edita um vínculo existente; só age em oportunidade sem vínculo |

Ao vincular, em uma transação: cria o vínculo (`link_source=auto`, `match_reason`, `cod_empresa`, `cod_vendedor`),
aponta `xprocess_num_pedido/documento` na oportunidade e grava o evento `xprocess_linked` (`source=xprocess`). Em
seguida roda a reconciliação normal desse vínculo na mesma rodada: preenche status, `realized_value` (Fase 4) e frete.
A oportunidade continua `convertida`, com `conversion_source=manual`. Depois da janela de 7 dias, ela segue
convertida e só pode ser vinculada à mão.

## Vínculo manual
- `PUT /sales-opportunities/{id}/xprocess-link` agora aceita oportunidade `convertida` (antes só `aberta`).
- Recusa (409) um pedido (mesmo número + documento) que já está vinculado a outra oportunidade.
- A lista de candidatos também funciona para `convertida` e compara a data por **data** (corrige a venda do mesmo dia
  ser descartada por `00:00 < hora da abertura`).

## Um pedido nunca alimenta duas oportunidades
Índice único parcial `idx_sales_opp_xlink_open_order` em `(organization_id, cod_empresa, num_pedido)` para vínculos abertos
com `cod_empresa` conhecido (criado só se não houver duplicados; o upgrade nunca falha por dado antigo), mais as checagens
de aplicação acima e uma guarda na reconciliação (outro vínculo já tem o pedido → não converte nem atualiza nenhum dos dois).

## Frete
`vl_frete` passa a ser lido; a **soma por pedido** vai para `valor_frete` no vínculo, exibido junto do pedido (painel do
contato e formulário do X2 da oportunidade). **Nunca entra em `valor_vendido` nem em `realized_value`**
(`total = subtotal − desconto`, sem frete). Sem relatório de frete nesta fase.

## Schema (`sales_opportunity_xprocess_links`, aditivo)
`valor_frete numeric` (nullable), `link_source varchar(10) NOT NULL DEFAULT 'agent'` (vínculos existentes ficam `agent`),
`match_reason text`; mais o índice acima. Tipo de evento novo `xprocess_linked`. Nenhuma permissão nova.

## Fora desta entrega
`/api/separacoes-produtos` (num_pedido repete entre lojas e anos); sincronização de vendedores para usuários;
`realized_quantity` automática; mapeamento Unit ↔ loja X2 (Fase 3B); contagem de `consecutive_not_found` por dia e
marca de última rodada persistida (correção independente do reconciliador).

## Riscos conhecidos
- O job roda a cada reinício do servidor depois das 2h (marca de dia só em memória): a descoberta é idempotente, só gasta chamadas.
- Cliente com mais de 1000 linhas nunca é associado automaticamente (candidato manual).
- A associação automática é conservadora por desenho: na dúvida, o agente decide.
