# Proposta de migração aditiva de permissões dos papéis de sistema

**Status: proposta. Nada foi executado nesta rodada** e nenhuma permissão foi concedida a nenhum papel.

## 1. Diagnóstico (medido no banco de desenvolvimento, 26/10/2026)

O papel de sistema `admin` da organização de teste tem 123 permissões; o código entrega hoje 127. As 4 ausentes são exatamente as permissões que **passaram a existir depois** da criação do papel (15/09/2026 14:47, sem nenhuma edição desde então: `updated_at = created_at`):

| Permissão | Criada na tabela `permissions` | O que concede | Risco |
|---|---|---|---|
| `occurrences:delete` | 16/09/2026 | excluir protocolo | **destrutivo** |
| `sales_opportunities:delete` | 28/09/2026 | excluir oportunidade (definitivo) | **destrutivo** |
| `sales_opportunities:view_team` | 14/10/2026 | ver o funil da própria equipe | leitura/supervisão |
| `analytics.agents:view_team` | 17/10/2026 | supervisionar agentes da própria equipe | leitura/supervisão |

`manager` e `agent` não têm permissão faltando (têm extras, que são personalizações). Os cinco perfis de equipe (SAC, Vendas, Logística, Supervisor de Loja, Supervisor de SAC) estão idênticos ao padrão.

**Causa:** `internal/database/postgres.go` (sincronização dos papéis de sistema) só liga permissões a um papel de sistema que **não tenha nenhuma** (`permCount > 0 → continue`, para "não sobrescrever personalizações"). Permissões novas entram na tabela `permissions` por semeadura, mas nunca são ligadas a papéis que já existiam. Organizações criadas depois do código novo recebem o conjunto completo (conferido: uma organização nova criada hoje recebeu 127 no `admin`). Não é falha de dado nem personalização: é ausência de um passo incremental.

## 2. Princípios

1. **Somente aditiva.** Nunca remover nem substituir permissão de papel algum.
2. **Somente papéis de sistema** (`admin`, `manager`, `agent`). Perfis de equipe e papéis personalizados não são tocados (continuam "criados uma vez, nunca sobrescritos").
3. **Respeitar remoção intencional.** Uma permissão só é candidata se não pode ter sido removida de propósito: `permissions.created_at > role.updated_at` (ela não existia quando o papel foi editado pela última vez). Se o papel foi editado depois da criação da permissão, a ausência pode ser deliberada: vai para revisão, não é adicionada.
4. **Permissões destrutivas nunca entram sozinhas.** `*:delete` exige decisão explícita do administrador da organização (excluir protocolo/oportunidade é irreversível e conflita com retenção e bloqueios legais). As de leitura/supervisão (`*:view_team`) podem entrar automaticamente.
5. **Dry-run primeiro, por organização, com relatório.** Nada executa sem aprovação.
6. **Reversível e auditável.** Snapshot prévio das ligações do papel e uma linha de auditoria por permissão adicionada.

## 3. Desenho

Comando administrativo (CLI, não automático no boot): `whatomate roles sync-defaults [--org <id>] [--dry-run] [--apply] [--include-destructive <perm,...>]`.

Para cada papel de sistema de cada organização:

```
faltantes   = padrão_do_código(role.name) − ligações_atuais
candidatas  = { p ∈ faltantes : p.created_at > role.updated_at }
revisão     = faltantes − candidatas          # possivelmente intencional
automáticas = candidatas − destrutivas        # *:delete
pendentes   = candidatas ∩ destrutivas        # exigem --include-destructive explícito
```

- `--dry-run` (padrão) imprime, por organização e papel: automáticas, pendentes (destrutivas) e revisão, sem escrever nada.
- `--apply` liga apenas as automáticas, numa transação por papel, grava snapshot em `role_permission_backups(org, role, permission, taken_at)` e registra no log de auditoria (quem, quando, quais).
- `--include-destructive occurrences:delete` liga a destrutiva nomeada, só para as organizações escolhidas.
- Idempotente: reexecutar não duplica (chave `(custom_role_id, permission_id)`).
- Invalidação: após aplicar, limpar o cache de permissões das organizações afetadas (o cache de `getUserPermissionsCached` é por usuário/organização).

Efeito esperado no caso do desenvolvimento: `admin` recebe automaticamente `sales_opportunities:view_team` e `analytics.agents:view_team`; `occurrences:delete` e `sales_opportunities:delete` ficam **pendentes de decisão**.

## 4. Verificação prévia em produção (somente leitura)

```sql
-- permissões do padrão que faltam em cada papel de sistema, com a data de criação de cada uma
SELECT o.name AS org, r.name AS role, r.updated_at AS role_updated, p.resource||':'||p.action AS perm, p.created_at AS perm_created,
       (p.created_at > r.updated_at) AS pode_ser_automatica
FROM custom_roles r JOIN organizations o ON o.id = r.organization_id
JOIN permissions p ON TRUE
WHERE r.is_system AND r.deleted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM role_permissions rp WHERE rp.custom_role_id = r.id AND rp.permission_id = p.id)
ORDER BY 1,2,4;   -- cruzar com SystemRolePermissions() do código para ignorar o que não é padrão do papel
```

Responder antes de qualquer execução: (a) as 4 permissões faltam também em produção? (b) algum papel de sistema foi editado depois de 16/09? (c) a organização quer `occurrences:delete` no administrador?

## 5. Testes exigidos antes de aplicar

- papel nunca editado recebe só as automáticas; papel editado depois recebe nada e aparece em "revisão";
- remoção intencional (editada após a criação da permissão) é preservada;
- `*:delete` nunca é ligada sem `--include-destructive`;
- perfis de equipe e papéis personalizados intactos;
- idempotência; isolamento entre organizações; snapshot e auditoria gravados; reversão a partir do snapshot restaura o estado anterior;
- usuário do papel passa a ter o acesso **somente** das permissões acrescentadas.

## 6. O que não fazer

Conceder as 4 permissões diretamente por SQL; ligar todas as permissões ao `admin` "para igualar"; sobrescrever perfis de equipe; executar no boot da aplicação.
