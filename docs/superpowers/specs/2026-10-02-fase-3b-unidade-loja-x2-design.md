# Fase 3B: vínculo administrativo Unit ↔ loja do X2

## Objetivo
Dar significado ao `cod_empresa` que já aparece nos pedidos X2 (hoje só um número): o administrador diz qual loja do
X2 cada `Unit` do Whatc representa. É um **cadastro explícito**, não uma sincronização.

## O que o X2 oferece (`GET /api/lojas`, amostra real: 32 lojas)
`cod_empresa`, `razao_social_empresa` (o nome da loja vem entre parênteses, às vezes com espaço duplo), `cnpj_empresa`
(com máscara), `data_referencia_dados`. **Sem campo de situação e sem paginação.** É um retrato D-1.

## Regras
| Regra | Detalhe |
|---|---|
| Quem | `units:write`. Não exige `xprocess_integration:read`: o vínculo é propriedade administrativa da `Unit`; a chave nunca chega ao usuário |
| Dado | `units.xprocess_cod_empresa` (nullable, `column:` explícito), **único por organização** entre unidades não excluídas (índice parcial `idx_units_org_xprocess_loja`). Excluir a unidade libera o código |
| Validação | o `PUT` consulta `GET /api/lojas` e só aceita um `cod_empresa` que existe **naquele momento**. Depois de salvo, o código é lido localmente: **nenhuma leitura de Unit, pedido ou contato consulta o X2** |
| Limpar | `cod_empresa: ""` remove o vínculo **sem consultar o X2** (funciona com o X2 fora do ar) |
| Sugestão | a lista traz `suggested_unit_*` por **nome** só como dica de apresentação, **nunca salva**. Duas camadas (nome igual; depois prefixo de palavras, ex. PORTO / PORTO SEGURO) e só quando a correspondência é única nos dois sentidos (loja ↔ unidade): nomes ambíguos (CAMACARI / CAMACARI 2, LAURO) não geram sugestão |
| CNPJ | opção `fill_cnpj`, **desmarcada por padrão**: copia o CNPJ da loja só para um CNPJ **vazio** da unidade; nunca substitui; valida; respeita a unicidade; em conflito/inválido **não altera o CNPJ e o vínculo é salvo mesmo assim**, com o motivo (`unit_has_cnpj`, `store_cnpj_invalid`, `cnpj_in_use`) |
| Auditoria | cada vínculo/troca/remoção gera registro de auditoria da unidade |
| Edição normal | `PUT /api/units/{id}` não toca no vínculo |

## Endpoints
- `GET /api/units/xprocess-lojas`: lojas do X2 com `unit_id`/`unit_name` (já vinculada) e `suggested_unit_id`/`suggested_unit_name`. 409 sem integração ativa; 502 se o X2 falhar.
- `PUT /api/units/{id}/xprocess-loja` `{cod_empresa, fill_cnpj}`: vincula, troca ou limpa. 400 código inexistente no X2; 409 outra unidade já usa a loja; 404 unidade de outra organização.

## Onde aparece
O vínculo X2 (`GET …/xprocess-link`) e os candidatos passam a trazer `unit_name`, resolvido **localmente**. O painel do contato e o formulário
do X2 mostram "Loja: PORTO SEGURO" ou, sem mapeamento, "Loja: X2 40". O pedido nunca depende do mapeamento.

## Fora da 3B
Descoberta automática, reconciliação, `consecutive_not_found`, criação automática de `Unit`, exclusão ou desativação automática e qualquer
sincronização destrutiva a partir da lista de lojas. A lista do X2 nunca cria, altera ou remove unidades sozinha.

## Schema (aditivo)
`units.xprocess_cod_empresa varchar(20) NULL` + índice único parcial. Validado em cópia de banco existente (as unidades atuais ficam sem vínculo; execução repetida sem erro).
