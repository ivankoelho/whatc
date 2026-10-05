# Permissões por grupo funcional

## Objetivo
A tela de permissões deixa de ser uma matriz técnica `resource/action`. O administrador vê a área funcional, o recurso, o que cada
permissão libera e liga/desliga com um switch.

## O que não muda
Autorização (`HasPermission`, `requireAuth`, guardas), tabelas `permissions`/`role_permissions`, as **117 chaves** `resource:action`, o seed e
os backfills, os papéis padrão e o payload de `POST/PUT /api/roles` (`permissions: ["resource:action"]`). O agrupamento é só apresentação.

## Decisões
| Tema | Decisão |
|---|---|
| Fonte dos metadados | Backend dona da **estrutura**: `models.PermissionPresentation(resource, action)` (`internal/models/permission_catalog.go`) devolve `group`, `group_order` e `sort_order`; `GET /api/permissions` os acrescenta a cada item. Nada é gravado no banco |
| Textos | Ficam no i18n (`permissionCatalog.groups/resources/permissions`, en + pt-BR, mesmas chaves). Motivo: o frontend é bilíngue e o seed nunca atualiza descrição de linha existente. Chave i18n = chave técnica com `.` e `:` trocados por `_` (`units:write` → `units_write`). Texto ausente cai na descrição do backend |
| Recurso não mapeado | Cai no grupo `other`: nenhuma chave some da tela |
| Grupos | `admin`, `service`, `crm`, `sales`, `whatsapp`, `ai`, `integrations`, `analytics`, `calls`. Telefonia (8 chaves) ganhou o grupo `calls` só de apresentação, porque as chaves existem e não podem sumir |
| Switch | `Switch` do projeto (`role="switch"`, `aria-checked`, teclado), `aria-label` = nome da permissão, texto Ligado/Desligado ao lado |
| Grupo | accordion; "N de M permissões ativas"; botão Ligar todas/Desligar todas só emite `update:selectedPermissions`: o salvamento continua no botão Salvar da função |
| Busca | nome, recurso, descrição e chave técnica; mantém os grupos, esconde só os vazios; abre os grupos com resultado |

## Testes
`internal/models/permission_catalog_test.go` (toda chave tem grupo real, 117 chaves, nenhum recurso em dois grupos), `roles_test.go`
(`ListPermissions` traz o grupo), `lib/permission-groups.spec.ts`, `components/roles/PermissionMatrix.spec.ts`, e2e `settings/roles.spec.ts`.
