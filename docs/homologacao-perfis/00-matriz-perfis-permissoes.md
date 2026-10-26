# Matriz de perfis e permissões — feature/team-role-profiles

Gerada em 26/10/2026 a partir do código (`models.SystemRolePermissions()` e `models.TeamProfiles()`) e do banco de desenvolvimento `whatomate_valida` (papéis `custom_roles` + `role_permissions`). **Efetivas** = o que está gravado hoje no banco; **padrão** = o que o código entrega a uma organização nova.

## 1. Papéis encontrados

| Perfil | Tipo | Efetivas | Padrão (código) | Faltam vs padrão | Extras vs padrão | Usuários (dev) |
|---|---|---:|---:|---:|---:|---:|
| admin | sistema | 123 | 127 | 4 | 0 | 2 |
| manager | sistema | 96 | 91 | 0 | 5 | 1 |
| agent | sistema | 22 | 21 | 0 | 1 | 26 |
| SAC | perfil de equipe (editável) | 20 | 20 | 0 | 0 | 1 |
| Vendas | perfil de equipe (editável) | 23 | 23 | 0 | 0 | 1 |
| Logística | perfil de equipe (editável) | 21 | 21 | 0 | 0 | 2 |
| Supervisor de Loja | perfil de equipe (editável) | 29 | 29 | 0 | 0 | 1 |
| Supervisor de SAC | perfil de equipe (editável) | 33 | 33 | 0 | 0 | 1 |

Papéis de teste E2E encontrados no mesmo banco (sem relação com os perfis de produção): "So Leitura *" (0–1 permissão) e "Supervisor Equipe *" (0–2 permissões), um usuário cada. Foram criados pelos testes E2E e não entram na homologação.

## 2. Conflitos e desvios (permissões herdadas, personalizadas e deriva)

Os perfis de equipe são criados **uma vez** por organização e nunca sobrescritos (decisão registrada em `team_profiles.go`): permissões acrescentadas ao código depois disso **não chegam** aos perfis já existentes. Resultado medido no banco de desenvolvimento:

- **admin**: faltam 4 (analytics.agents:view_team, occurrences:delete, sales_opportunities:delete, sales_opportunities:view_team).
- **manager**: nada falta; 5 extra(s) (occurrences.processes:delete, occurrences.processes:read, occurrences.processes:write, xprocess_integration:read, xprocess_integration:write).
- **agent**: nada falta; 1 extra(s) (contacts:write).
- **SAC**: idêntico ao padrão.
- **Vendas**: idêntico ao padrão.
- **Logística**: idêntico ao padrão.
- **Supervisor de Loja**: idêntico ao padrão.
- **Supervisor de SAC**: idêntico ao padrão.

Observações:
- Os cinco perfis de equipe estão **idênticos ao padrão do código** no banco de desenvolvimento: nenhuma permissão foi concedida a eles para a homologação.
- O papel de sistema **admin** do banco de desenvolvimento está **sem 4 permissões** que o código lhe dá hoje (a sincronização dos papéis de sistema não as trouxe): é deriva de ambiente, não de código, e não afeta os cenários de homologação; deve ser conferido antes de qualquer deploy.
- **manager** e **agent** têm permissões extras no banco (acrescentadas por administradores ou por versões anteriores): são personalizações, não conflito.
- Não existe, no modelo atual, permissão por usuário: o efetivo de uma pessoa é o do seu papel (mais o vínculo à equipe/loja, que restringe a visibilidade, nunca amplia).
- `occurrences:view_all` (supervisionar casos) é diferente de `conversations:view_all` (ler conversas de WhatsApp de toda a organização): só o primeiro é dado ao Supervisor de SAC.

## 3. Capacidades por perfil (permissões efetivas)

| Capacidade | Permissão | admin | manager | agent | SAC | Vendas | Logística | Supervisor de Loja | Supervisor de SAC |
|---|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| Abrir/ver ocorrências (protocolos) | `occurrences:read` | sim | sim | sim | sim | sim | sim | sim | sim |
| Criar/editar ocorrências | `occurrences:write` | sim | sim | sim | sim | sim | sim | sim | sim |
| Supervisionar TODAS as ocorrências | `occurrences:view_all` | sim | sim | — | — | — | — | — | sim |
| Administrar etapas do pipeline | `occurrences.stages:write` | sim | sim | — | — | — | — | — | — |
| Chat interno: ler | `team_chat:read` | sim | sim | sim | sim | sim | sim | sim | sim |
| Chat interno: escrever | `team_chat:write` | sim | sim | sim | sim | sim | sim | sim | sim |
| Chat interno: administrar | `team_chat:manage` | sim | sim | — | — | — | — | — | sim |
| WhatsApp: ler conversas | `chat:read` | sim | sim | sim | sim | sim | sim | sim | sim |
| WhatsApp: responder | `chat:write` | sim | sim | sim | sim | sim | sim | sim | sim |
| Conversas de toda a organização | `conversations:view_all` | sim | sim | — | — | — | — | — | — |
| Conversas da própria equipe | `conversations:view_team` | sim | — | — | — | — | — | sim | sim |
| Atribuir conversas | `chat.assign:write` | sim | sim | — | — | — | — | sim | sim |
| Contatos: ler | `contacts:read` | sim | sim | sim | sim | sim | sim | sim | sim |
| Contatos: editar | `contacts:write` | sim | sim | sim | — | sim | sim | sim | sim |
| Funil de vendas: ler | `sales_opportunities:read` | sim | sim | sim | — | sim | — | sim | sim |
| Funil: ver a equipe | `sales_opportunities:view_team` | — | — | — | — | — | — | sim | — |
| Usuários: ler | `users:read` | sim | — | — | — | — | — | — | sim |
| Configurações gerais | `settings.general:read` | sim | sim | — | — | — | — | — | — |
| Antimalware (admin) | `settings.antimalware:read` | — | — | — | — | — | — | — | — |
| Retenção (admin) | `settings.retention:read` | — | — | — | — | — | — | — | — |
| Logs de auditoria | `audit_logs:read` | sim | — | — | — | — | — | — | — |

Legenda: **sim** = permissão efetiva gravada; — = não possui. O administrador tem acesso a tudo pelo papel de sistema.

## 4. O que cada perfil pode e não pode (regras do servidor)

| Perfil | Pode | Não pode |
|---|---|---|
| SAC | Abrir e tratar ocorrências do seu escopo, encaminhar para Vendas/Logística, usar a discussão interna do caso, anexar arquivos, adicionar apoio de departamentos permitidos | Ver ocorrências de outra loja/equipe sem vínculo; supervisionar tudo; administrar etapas/antimalware/retenção |
| Vendas | Idem no escopo da loja; funil de vendas | Ver conversas/ocorrências de outras lojas |
| Logística | Receber encaminhamentos, tratar, devolver, anexar; contatos da base | Ver o WhatsApp do cliente de um caso em que só participa da discussão; arquivos restritos de outros |
| Supervisor de Loja | Acompanhar e intervir nos casos da sua loja/equipe; atribuir conversas | Ver casos de outra loja; administrar o sistema |
| Supervisor de SAC | Supervisionar casos de todas as lojas (`occurrences:view_all`), administrar o chat interno do caso | Ler conversas de WhatsApp de contas/equipes a que não pertence (`conversations:view_all` não concedido) |
| Administrador | Tudo, incluindo etapas, antimalware, retenção, auditoria | — |
| Pessoa de apoio / acompanhando (vínculo no caso, qualquer perfil) | Abrir o caso e comentar | WhatsApp do cliente, arquivos restritos, outros protocolos (`TestSupport_GrantsNoWhatsAppRestrictedFilesOrOtherCases`) |

## 5. Testes automatizados existentes que cobrem permissões

`team_profiles_test` (conteúdo dos perfis), `occurrence_permission_scope_test` e `occurrence_isolation_test` (escopo por loja/equipe/organização), `occurrence_participants_test` e `occurrence_support_test` (vínculos), `occurrence_case_export_test` / `occurrence_report_test` (o que cada pessoa pode exportar), `chat access/authz matrix` (Chat Interno), `filescan_test` e `file_preview_test` (anexos, antimalware, visualização), `retention` (bloqueios e retenção) e `occurrence_stages_reorder_test` (permissão de ordenar etapas).

Os resultados da homologação por perfil, no navegador e por API, estão em `01-homologacao-cruzada.md`; as lacunas conhecidas, no fim daquele documento.
