# Revisão de integração e segurança pré-merge — feature/team-role-profiles

Data: 27/10/2026. Revisão apenas de leitura e testes; nenhum merge, deploy, migração em produção, LDAP, expurgo ou alteração de outras branches. Comparação com `origin/development` (`d5bfeb31`); o `development` **local** (`b0d3c2d1`) está atrás do remoto e não foi usado.

## 1. Resumo das alterações

- **111 commits exclusivos**, **240 arquivos** (+27.857 / −872), sem mudança em `go.mod`/`go.sum`/`package.json` (**nenhuma dependência nova**). Distribuição: ~103 em `internal/handlers`, ~34 em `frontend/src` (mais subpastas), 11 em `internal/models`, 5 em `internal/database`, 4 em `pkg/`/`worker`/`websocket`, 2 de homologação (`docker/homologation`, `scripts/homologation`).
- Temas (commits relevantes): perfis de equipe e permissões (`ecdf793e`, `83ebc99b`, `341f5c76`); ocorrências com estados gerais, pipelines por equipe, encaminhamento, participantes (`0814fc6b`…`d9ea6bae`, `5f9e75ce`); automações e comunicação ao cliente (`2087ba7c`, `bcd0ccb9`); discussão interna do caso no Chat Interno (`78a41107`, `3ff54fc3`, `7f99228f`); documentos, NF-e, Nextcloud e exportação (`0f8d0982`, `e6055eaf`); antimalware ClamAV com bloqueio de todos os caminhos de arquivo (`352064dc`, `77dd1121`, `03ab913f`); correção de SSRF e telefonia (`623c1a30`, `afe42212`); retenção preparada e **desligada** (`49afe511`…`e308642c`); e, nesta etapa, prévia separada de download (`6d203495`), acompanhamento e apoio por departamento (`3bc9d517`, `c0390e79`), ordenação de etapas (`821e1029`), padrão de datas (`45924497`), relatório PDF completo (`1e173687`), **Encaminhar/Devolver** (`a7aebdc8`, `ff88b08a`) e as correções do pool de conexões (`94488e9d`) e do nome da equipe em tempo real (`c4a0b0eb`).

## 2. Compatibilidade com `development` (conflitos)

`development` tem 12 commits novos desde a base (todos da proteção de códigos OTP, PR #138). Um merge de teste (`git merge-tree`, sem escrever nada) mostra **6 arquivos em conflito**:

| Arquivo | Natureza |
|---|---|
| `cmd/whatomate/main.go` | **textual**: ambos adicionam subcomandos de CLI (`otp-*` e `antimalware`); manter os dois |
| `config.example.toml` | **textual**: seções `[otp]` e `[antimalware]`; manter as duas |
| `internal/worker/worker.go` | **semântico**: o desenvolvimento desvia códigos OTP para `sendSecretRecipient`; a feature bloqueia mídia de campanha infectada. As duas verificações devem coexistir, **na ordem certa** (a do OTP primeiro não pode pular a checagem de mídia infectada) |
| `internal/handlers/messages.go` | **semântico**: o desenvolvimento mascara o código de autenticação nos metadados; a feature copia `ExtraMetadata` para a mensagem. Copiar metadados arbitrários **depois** do mascaramento pode reintroduzir o código; precisa de resolução cuidadosa |
| `pkg/whatsapp/types.go` e `client.go` | **semântico**: os dois lados mudaram o tipo `APIError` (`Status/Code/msg` contra `StatusCode/Msg`) e o tratamento de erros de transporte (`redactError` contra `TransportError`); a resolução deve preservar a **redação de segredos** do desenvolvimento e o contrato de erro que o restante da feature consome |

Recomendação: antes do merge, **integrar `origin/development` na feature em uma etapa própria** (commit de reconciliação), resolver os conflitos semânticos com revisão de segurança das 3 áreas OTP/mídia/redação de erro, recompilar e rodar a suíte completa. Isso altera a feature, não outras branches, e **não foi feito** nesta tarefa.

## 3. Migrações (executam no bloco `-migrate` do boot, nesta ordem)

1. **AutoMigrate** de tabelas novas (aditivo): `OccurrenceParticipant` (colunas novas: `role`, `department_id`, `removed_by_id`), `OccurrenceTransfer`, `OccurrenceChannelConfig`, `OccurrenceNotification(+Attempt)`, `ChatPollVote`, `OccurrenceFile`, `NextcloudSettings/Credential`, `FileScan`, `MetaMedia`, `RetentionPolicy/Hold/HoldEvent/Run/Item`; colunas novas em ocorrências, etapas e canais.
2. **SQL idempotente** (`IF NOT EXISTS`/`DROP INDEX IF EXISTS`): índices únicos parciais de nome de etapa (por organização e por equipe), de automação (por organização/unidade/equipe e evento) e `idx_chat_channel_occurrence` (uma discussão por caso). `UPDATE`s de classificação de etapas e casos que só preenchem o que está vazio.
3. **Seed de perfis de equipe** (`SeedTeamProfilesForOrg`): cria o que falta por nome, **nunca sobrescreve**.
4. **`BackfillFileScans`** (lotes de 5000, idempotente): registra **todo arquivo existente** como `pending`. **Impacto operacional:** arquivo sem veredito limpo é bloqueado; sem `antimalware.address` configurado nada é varrido e **anexos e mídia de WhatsApp antigos ficam inacessíveis** até o ClamAV varrer (ritmo `batch_size = 5` por 10 s por padrão: pode levar muito tempo em bases grandes; ajustar `batch_size`). É requisito de implantação.
5. **`BackfillOccurrenceViewAll`** (aditivo, uma vez por organização: pula organização onde algum papel já tem a permissão) e **`ReconcileSupervisorSACPreset`** (**estreita** `conversations:view_all` → `view_team` somente em papel "Supervisor de SAC" que seja idêntico ao antigo preset; personalizados ficam). Esta é a única etapa que **retira** permissão: só de papel não modificado, em transação por papel.

Avaliação: compatível com bancos existentes (aditivas e idempotentes; reexecução segura). Locks: criação de índices únicos em tabelas grandes (`occurrences`, `occurrence_stages`, `chat_channels`) usa `CREATE UNIQUE INDEX` sem `CONCURRENTLY` → bloqueia escrita durante a criação; as tabelas de ocorrências são pequenas hoje, mas **conferir o tamanho em produção**. Backup: **obrigatório antes** (por causa de 4 e 5). A tabela `occurrence_transfers` é nova e o fallback de casos antigos usa o evento "encaminhado". A **migração de permissões do admin** (`02-proposta-migracao-permissoes.md`) é separada e **não é executada** por nada acima.

## 4. Mudanças de API e contrato frontend/backend

Rotas novas (todas atrás do grupo autenticado; os handlers conferem permissão e escopo): `/api/occurrences/{id}/{files…, participants, support-options, transfers, forward, return, claim, reassign, history, export, whatsapp, chat, notifications}`, `/api/occurrence-stages/reorder`, `/api/retention/*`, `/api/integrations/{antimalware,nextcloud}`, `/api/media-status`, `/api/contacts/find`, polls e Nextcloud no Chat Interno. Alterações de comportamento de rotas existentes: lista de participantes agora inclui vínculos removidos (`active:false`) e novos campos; participante adicionado sem capacidades recebe `comment`; `forward-targets?team_id=` devolve membros; exportação PDF com novo layout; `formatDate/formatTime/formatDateTime` do frontend passam a seguir o idioma e o fuso da organização (inglês inalterado). O frontend e o backend desta branch foram alterados juntos; **não há compatibilidade retroativa com um frontend antigo** (ex.: lista de participantes).

## 5. Configuração e variáveis de ambiente necessárias

- `[antimalware] address` (e demais chaves), **obrigatório** para liberar qualquer arquivo; `docker/docker-compose.clamav.yml` (porta 3310 só em loopback em desenvolvimento; em produção sem publicar a porta).
- `[whatsapp] automation_sending_enabled = false` (padrão): nenhuma comunicação automática sai até o checklist de consentimento.
- `[app] allow_private_nextcloud` apenas desenvolvimento; `occurrence_file_max_mb` (15).
- Retenção: `purgeExecutionEnabled = false` é **constante de compilação**, e as políticas são `Enabled = false` por padrão: o expurgo não executa.
- Com a integração do `development`: `[otp]` (`max_validity_minutes`, `require_valid_until`, `allow_local_release`).

## 6. Revisão de segurança (do que a branch altera)

| Área | Verificação | Resultado |
|---|---|---|
| RBAC efetivo | testes com perfis reais; `occurrences:view_all` separado de `conversations:view_all`; apoio/acompanhando não herdam | OK (12 cenários, 468 verificações; negativos 13/13) |
| Isolamento organização/loja/equipe | outra loja, outra organização, sem vínculo: caso, lista, anexos, URL direta, WebSocket | OK |
| Acesso após encaminhar | equipe anterior perde; quem encaminha só vê/comenta; novo responsável recebe | OK (F2–F6) |
| Apoio e acompanhando | sem WhatsApp, sem anexo restrito, sem outros protocolos, sem encaminhar/redistribuir | OK |
| Chat Interno e WebSocket | acesso derivado do caso; broadcast só a quem passa no autorizador; sem vazamento a outra loja/organização | OK; **corrigido** esgotamento do pool em escritas concorrentes |
| Anexos | prévia só inline para PDF/imagem/texto, tipo pelos bytes, CSP `sandbox`, `nosniff`, sem HTML/SVG ativo; download exige permissão **e** veredito limpo (423 caso contrário) | OK; veredito *infected* real não demonstrado neste host (ver 8) |
| Exportação | seção por permissão, 403 explícito, WhatsApp só a quem lê, ZIP só com arquivo limpo | OK |
| Auditoria | exportação e arquivos escrevem em `audit_logs`; **Encaminhar/Devolver, participantes e reordenação de etapas não escrevem em `audit_logs`** (encaminhar/participantes ficam na linha do tempo append-only; **reordenar etapas não deixa registro nenhum**) | **Pendência média** (não bloqueia merge) |
| Retenção | expurgo desligado por constante; simulação somente leitura; bloqueios legais impedem exclusão de arquivo | OK |
| Exposição de nomes | `forward-targets?team_id=` lista nomes de membros ativos de qualquer equipe a quem tem `occurrences:write` (dentro da organização, entre lojas) | **Pendência baixa/média**: avaliar restringir às equipes encaminháveis |
| Conflito OTP/mídia/erros | ver seção 2 | **Bloqueante para merge até a integração** |

Regressão em recursos anteriores: suíte Go `handlers` com 2001 aprovados e só as 3 falhas preexistentes; 24 pacotes `ok`; vitest 458 aprovados; `vue-tsc` limpo. Um defeito herdado do Chat Interno (pool de conexões) foi corrigido e coberto por teste.

## 7. As três falhas Go preexistentes (reexecutadas e analisadas)

| Teste | Causa verificada | Impacto funcional / segurança | Risco em produção | Quando corrigir |
|---|---|---|---|---|
| `TestServeLoginBackground_RejectsSymlink` | O teste falha em `os.Symlink` (**Windows sem privilégio para criar symlink**), antes de exercer o produto. **Rodado em Linux (golang:1.26, mesmo banco): PASSA.** A proteção está em `resolveSafeStoragePath` (`os.Lstat` + `ModeSymlink`) | Nenhum no produto. **Segurança:** a proteção (leitura de arquivo arbitrário via symlink) existe e é verificada em Linux | Nenhum identificado (produção é Linux) | Não bloqueia merge nem deploy; rodar sempre em Linux/CI; no Windows, pular com `t.Skip` quando `os.Symlink` falhar |
| `TestApp_ServeMedia_RejectsSymlink` | Idem (mesma causa e mesma proteção em `ServeMedia`). **Linux: PASSA** | Idem | Nenhum identificado | Idem |
| `TestUpdateContactChatbotMessage_SetsTimestampAndResetsReminder` | **Teste instável, também no Linux (3 falhas em 5)**: compara `before := time.Now()` (nanossegundos) com o timestamp lido do banco (microssegundos), e o valor gravado pode ficar abaixo de `before` por arredondamento | Nenhum no produto: o horário é gravado corretamente; é a asserção que é rígida demais. Sem impacto de segurança | Nenhum | Não bloqueia merge; corrigir o teste (truncar `before` ao microssegundo) antes de depender da suíte verde como critério de implantação |

Nenhuma das três foi tratada como inofensiva por suposição: as duas de symlink foram confirmadas aprovadas em Linux; a terceira foi reproduzida como instável em Linux.

## 8. Pendências e riscos

**Bloqueantes para merge**
1. **Integrar `origin/development` na feature** e resolver os 6 conflitos (3 deles semânticos e sensíveis: OTP, mascaramento de metadados, redação de erros), com revisão de segurança dessas áreas, recompilação e suíte completa. Sem isso o merge não é seguro.

**Bloqueantes apenas para produção**
1. Executar/aprovar a migração aditiva de permissões do administrador (proposta separada, não executada) e conferir os papéis de produção.
2. Configurar e homologar o ClamAV de produção (inclusive veredito *infected* real, que o Windows Defender impediu de demonstrar aqui) antes de subir: sem ele, anexos e mídia ficam bloqueados; planejar a janela de varredura inicial (`BackfillFileScans`).
3. Backup do banco, tamanho das tabelas para os índices únicos sem `CONCURRENTLY`, ajuste do pool e do disco do Postgres sob escrita concorrente, WebSocket atrás do proxy, plano de implantação e reversão.
4. Corrigir a instabilidade do teste de timestamp e adicionar `t.Skip` aos testes de symlink no Windows (higiene do critério de aceitação da suíte).

**Pendências não bloqueantes**
- Auditoria em `audit_logs` para Encaminhar/Devolver, participantes e reordenação de etapas.
- Restringir `forward-targets?team_id=` quanto aos nomes de membros entre lojas.
- Homologar com vários navegadores simultâneos e repetir o cenário de três departamentos no navegador.

## 9. Recomendação

**NÃO APTO para merge neste momento; APTO para avaliação de merge assim que `origin/development` for integrado e os conflitos forem resolvidos e revalidados.** Funcionalidade, RBAC, isolamento, anexos/antimalware, retenção (desligada) e regressões estão consistentes e documentados; a única barreira técnica ao merge é a divergência com o `development` (OTP e redação de erros), que é uma reconciliação, não um defeito da feature. Deploy continua bloqueado pelos itens "apenas para produção".
