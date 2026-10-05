# Importar unidades do X2

## Objetivo
Criar no Whatc as `Unit` das lojas que já existem no X2. É a operação inversa do vínculo da Fase 3B:

| | Vincular (3B) | Importar |
|---|---|---|
| Parte de | uma unidade que já existe | uma loja do X2 que ainda não tem unidade |
| Faz | escolhe a loja da unidade (`PUT /api/units/{id}/xprocess-loja`) | cria a unidade da loja (`POST /api/units/xprocess-import`) |
| Tela | `UnitXProcessLojaDialog.vue` (ícone de vínculo na linha) | `UnitXProcessImportDialog.vue` (botão "Importar do X2") |

As duas coexistem e usam a mesma identidade: **organização + `xprocess_cod_empresa`** (índice único parcial `idx_units_org_xprocess_loja`, que já existia: nenhuma migration).
Nome e CNPJ nunca identificam a loja.

## Origem dos dados
`GET /api/lojas` do X2 pelo cliente existente (`xprocess.Client.ListarLojas`) com a credencial da organização (`fetchXProcessLojas`: integração ativa, chave decifrada no servidor, timeout de 20 s, resposta limitada a 1 MiB). O frontend só chama o Whatc; a chave nunca sai do servidor. `ListarLojas` passou a recusar `{"ok": false}` (antes virava "nenhuma loja").

## Regras
| Regra | Detalhe |
|---|---|
| Quem | `units:write` (sem permissão nova) |
| Mapeamento | `cod_empresa` → `xprocess_cod_empresa`; nome → `name`; `cnpj_empresa` (só dígitos, se válido) → `cnpj`; `active = true`. `type`, endereço e demais campos ficam vazios |
| Nome | o nome da loja exatamente como está no X2 (`ATACADAO DOS PISOS LTDA  (PORTO)` → `ATACADAO DOS PISOS LTDA (PORTO)`), sem renomear nem abreviar; só se colapsam espaços repetidos e se tiram os das pontas. Atualizado em 2026-10-05: antes usava só o texto entre parênteses |
| Idempotência | loja cujo código já está numa unidade da organização **não cria outra** (`already_exists`) |
| Reimportar | nada local é sobrescrito: `name`, `type`, `active` e o resto ficam. A única escrita é preencher um CNPJ local **vazio** com o da loja (`updated`), se nenhuma outra unidade já o usar. CNPJ local diferente do X2 só gera a nota `cnpj_differs` |
| Conflito | não é resolvido sozinho: outra unidade **sem vínculo com a loja** que tenha o mesmo CNPJ (`cnpj_in_use`) ou o mesmo nome, sem diferenciar maiúsculas (`name_in_use`). Nada é criado e a resposta traz a unidade existente |
| CNPJ da loja inválido | a unidade é criada sem CNPJ, com a nota `store_cnpj_invalid` |
| Lote | cada loja é gravada sozinha; um conflito não desfaz as outras. Máximo de 200 por chamada; códigos repetidos viram um |
| X2 fora do ar / resposta inválida / sem integração | 502 / 502 / 409, **nada é gravado** |
| Corrida | se o índice único recusar na gravação (importação simultânea), a resposta vira `already_exists` ou `conflict`; nunca duplica |
| Auditoria | pelo mecanismo existente (`logAudit`, tabela `audit_logs`): uma entrada `created` por unidade criada (e `updated` quando o CNPJ é preenchido) com a mudança extra `source = xprocess_import` |

## API
`POST /api/units/xprocess-import` `{"cod_empresa": ["002", "003"]}` →
`{"summary": {selected, created, updated, already_exists, conflicts, failed}, "results": [{cod_empresa, status, reason?, notes?, x2_store, unit?, existing_unit?}]}`
com `status` em `created | updated | already_exists | conflict | failed` e `reason` em `cnpj_in_use | name_in_use | not_in_x2 | create_failed`.

`GET /api/units/xprocess-lojas` ganhou, por loja: `import_status` (`new | already_imported | conflict`), `import_name` (o nome que a unidade receberia), `conflict_reason`, `conflict_unit_id` e `conflict_unit_name`. A listagem e a importação usam a mesma função (`classifyLoja`), então a tela e o servidor não divergem.

## Fora desta entrega
Sincronização contínua, desativar ou excluir unidade por causa do X2, importar tipo ou endereço.
