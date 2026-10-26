# Retenção e ciclo de vida dos arquivos

Estado: **configuração, simulação e suspensões prontas; exclusão automática DESLIGADA e impossível de ligar por configuração.** Nada é apagado por esta funcionalidade. Parte 2 de `2026-10-26-antimalware-e-retencao-plano.md`, agora implementada até a simulação.

## Retenção de dados ≠ retenção de arquivos

A política governa **arquivos guardados pelo WhatC** (o anexo, a mídia, a cópia local). Ela **não** apaga: o histórico e os eventos do protocolo, o registro do documento (número, itens, datas), a mensagem, a campanha, os registros de auditoria. Um anexo pode sair enquanto o histórico e a auditoria permanecem, com os dados pessoais minimizados conforme a política aplicável. Os registros de auditoria **nunca** são apagados junto com arquivos e exigem política própria. O pedido de exclusão de um titular (LGPD) **não** é autorização automática: obrigação legal, contratual ou litígio prevalecem, e a suspensão (legal hold) é o instrumento para isso.

## Matriz do ciclo de vida

| Categoria | Armazenamento | Dono / vínculo | Quem acessa | Referências compartilhadas | Exclusão hoje | Política |
|---|---|---|---|---|---|---|
| Documentos de protocolos | `occurrence-files/<org>/<caso>/<id>` + `occurrence_files` | organização, protocolo | permissões do caso | pouco provável (id no nome) | só pelo usuário (remover arquivo / excluir protocolo); nenhuma automática | **configurável** (`occurrence_files`) — termo a partir do **encerramento** do protocolo |
| NF e cupom (anexo) | `occurrence-documents/<id>.ext` + coluna no registro | organização, protocolo | permissões do caso | não | ao excluir o protocolo; troca de anexo | **configurável** (`occurrence_invoices`) — só o arquivo; o registro permanece |
| Anexos do Chat Interno | `team-chat/<org>/<canal>/<id>.ext` + `chat_attachments` | organização, canal (e protocolo, se for discussão de caso) | membros do canal | não | **automática já existente**: arquivos de mensagens apagadas após 7 dias; pendentes abandonados | **configurável** (`chat_attachments`); discussão de protocolo segue o encerramento do caso; a limpeza existente **respeita suspensão** |
| Mídias do WhatsApp | `images|videos|audio|documents/` + `messages.media_url` | organização, contato | visibilidade da conversa | **sim**: mídia de campanha e de template reaproveitam o mesmo caminho | nenhuma | **configurável** (`whatsapp_media`) — só o arquivo; a mensagem permanece |
| Arquivos importados do Nextcloud | **cópia** em `occurrence_files` / `chat_attachments` | quem importou | como a categoria de destino | o **original** fica no Nextcloud | — | segue a categoria onde foi gravada; o original é **fora** (EX) |
| Campanhas / templates | `campaigns/<id>.ext` (cópia local p/ prévia) + `header_media_local_path` | organização | `campaigns:*` | **sim** (mensagens de campanha apontam para o mesmo arquivo) | nenhuma | **configurável** (`campaign_media`) — só a cópia local; mídia na Meta é **fora** |
| Áudios de URA | `audio/` | organização (ou sistema) | `ivr_flows:*`, **em uso** por fluxos | — | nenhuma | **fora** (configuração em uso) |
| Gravações de chamadas | S3 `recordings/<org>/<id>.ogg` | organização | `calls:*` | não | nenhuma | **fora** (armazenamento de objetos, precisa de regra própria) |
| Relatórios exportados | não guardados (memória) | — | — | — | — | **fora** (nada a reter) |
| Registros de auditoria | banco `audit_logs` | organização | `audit_logs:read` | — | nenhuma | **fora, nunca com arquivos** (política específica) |
| Mídia hospedada na Meta | Meta | Meta | — | — | impossível pelo WhatC | **fora** |

## Modelo

- `retention_policies` (organização × categoria): `keep_months`, `enabled`, quem alterou. **Sem prazo padrão**; sem linha ou `enabled=false` = nada elegível. Validação: 1–600 meses; não se ativa sem prazo. Migração: organizações existentes não ganham linha nenhuma, então nada muda para elas.
- `retention_holds` + `retention_hold_events`: suspensão por **protocolo** (cobre documentos, NF e discussão do caso) ou **contato** (mídia do WhatsApp e todos os protocolos dele). Justificativa obrigatória (≥10 caracteres) na aplicação e na revogação; autor e data; histórico só de acréscimo; nunca é apagada, só revogada.
- `retention_runs` / `retention_items`: ledger do motor (idempotente por organização+categoria+recurso).
- Regras de elegibilidade (primeiro que casa): **suspensão** → **quarentena** (infectado fica como evidência de segurança) → **referência compartilhada** → **protocolo aberto** (o prazo não começou) → **no prazo** → **elegível**.

## Simulação (somente leitura)

`POST /api/retention/simulate`: por categoria, a regra aplicada, a contagem e o volume dos elegíveis, os que estão no prazo, em protocolo aberto, **suspensos**, **compartilhados** e em **quarentena**, e amostras com o motivo e o protocolo relacionado. Aceita **prazo hipotético** (sem salvar). Não escreve nada (nem log), não remove, não enfileira — testado contando linhas de 11 tabelas e conferindo que o arquivo continua baixável. Volume: do banco quando existe, senão medido em disco (só leitura, até 20 000 arquivos; marcado como parcial quando passa disso).

## Suspensão efetiva

Enquanto ativa: nada do escopo é elegível; **excluir o protocolo** e **remover seus arquivos** respondem 423; a **limpeza automática do chat que já existia** (arquivos de mensagens apagadas) não toca nos anexos da discussão do caso. A revogação devolve tudo ao fluxo normal.

## Motor de expurgo (preparado, desligado)

`purgeExecutionEnabled` é uma **constante falsa**: nenhuma configuração, requisição ou variável de ambiente a liga, e nada inicia o motor. Sem execução ele só **planeja** (escreve apenas o ledger). A arquitetura de execução, testada contra um *store* falso por um construtor que só existe em testes:

- um lote pequeno por organização e categoria; claims com `FOR UPDATE SKIP LOCKED` e lease → dois workers nunca pegam o mesmo item;
- **reverificação por item no momento da remoção** (política ligada, prazo vencido, sem suspensão, sem referência compartilhada, sem quarentena);
- ordem segura: **registro no banco antes do arquivo**; se o arquivo falhar, o item fica `record_deleted` e só o arquivo é repetido (o registro não é removido de novo); falha no banco = `failed` com recuo exponencial;
- idempotência (planejar ou executar de novo não repete nada), pausa e retomada, métricas por status, auditoria por remoção;
- o *store* de produção (`fsPurgeStore`) existe, recusa caminhos fora da raiz de armazenamento e trata arquivo ausente como feito, mas **não está ligado a nada**.

## Interface

Integrações > **Retenção** (permissão `settings.general`): aviso permanente de que a exclusão automática está desativada; uma linha por categoria (interruptor, prazo, salvar; texto da regra); **Simular expurgo** (usa o prazo digitado, salvo ou não) com tabela e amostras; suspensões ativas com **Nova suspensão** / **Revogar** (justificativa); histórico (regras, suspensões e execuções); lista do que está fora da política. pt-BR e en. A suspensão por **contato** existe na API; a tela aplica por **protocolo**.

## Segurança e isolamento

Tudo é por organização (políticas, suspensões, simulação, ledger); outra organização não vê, não aplica nem revoga. Leitura exige `settings.general:read`; alterar regra/suspensão exige `settings.general:write` (as permissões personalizadas dos papéis não foram alteradas). Mudanças de regra e de suspensão são auditadas.

## Limitações

- Sem exclusão: nada foi (nem pode ser) removido; falta decidir os pontos abaixo antes de ligar.
- A suspensão por contato não tem tela; só API.
- "Protocolo encerrado" usa `closed_at`; protocolos que nunca fecham nunca vencem (por desenho).
- Mídias do WhatsApp não têm vínculo com protocolo: só a data da mensagem e a suspensão por contato as protegem.
- O volume de categorias sem tamanho no banco é uma medição em disco com teto.
- A política de **quarentena** (infectados) hoje só os mantém; não há prazo para eles.

## Decisões necessárias antes de habilitar a exclusão

1. **Prazos por categoria**, validados juridicamente (LGPD, fiscal, contratual); quem aprova (administrador + segundo aprovador?).
2. O que acontece com o **registro** quando o arquivo sai (marcador "arquivo expirado" visível ao usuário; texto do histórico do protocolo).
3. **Aviso prévio** aos responsáveis (e-mail/painel) e janela entre planejar e executar; exigir uma **simulação aprovada** antes da primeira execução.
4. Política para **quarentena** e para **arquivos pendentes/com erro** de varredura.
5. **Backup e restauração**: o expurgo precisa conversar com a retenção dos backups.
6. Política de **auditoria** e de **gravações** (fora desta etapa).
7. Direito de exclusão do titular: fluxo próprio, com análise de obrigação legal, separado do expurgo por prazo.
8. Quem pode **revogar** uma suspensão (hoje `settings.general:write`; talvez um papel dedicado).
9. Referências compartilhadas: aceitar "contagem de referências" ou copiar-na-escrita antes de ligar.
10. Habilitar em **uma organização** (piloto) antes das demais.

## Testes

`retention_test.go`, `retention_holds_test.go`, `retention_engine_test.go` (Go, banco e Redis isolados): configuração por organização e permissões; padrões desligados; validação; elegibilidade (vencido, no prazo, protocolo aberto, política desativada, prazo hipotético); referências compartilhadas e quarentena; categorias de WhatsApp e campanha (mídia só na Meta não é candidata); simulação sem efeitos colaterais; suspensão (aplicar, duplicada, revogar, histórico, auditoria, contato, bloqueio de exclusão manual e da limpeza do chat); motor (produção não executa, planejamento idempotente, reverificação, ordem, falhas parciais, retentativas, concorrência de 4 workers sobre 24 itens, pausa/retomada, isolamento, *store* de arquivos); frontend (`RetentionSettingsView.spec.ts`). Migração: a base de desenvolvimento recebeu as cinco tabelas sem tocar nos dados e sem linha de política.
