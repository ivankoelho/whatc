# Homologação cruzada entre departamentos — feature/team-role-profiles

Data: 26/10/2026. Ambiente: banco de teste isolado (`whatomate_test_profiles`, Redis db 5, `-p 1`) e servidor local de desenvolvimento (`whatomate_valida`). Nenhum WhatsApp real, nenhuma credencial de produção. A matriz de perfis está em `00-matriz-perfis-permissoes.md`.

Critério de classificação: **APROVADO** só quando o que a homologação exigia foi de fato observado. Passar apenas no teste automatizado, quando se exigia navegador, não é APROVADO: fica "automatizado aprovado / navegador NÃO EXECUTADO".

## 1. Suíte Go completa (banco e Redis isolados)

Rodada final: 24 pacotes `ok`; 3 falhas, todas **conhecidas e anteriores a este trabalho** (nenhuma regressão):

| Teste | Natureza |
|---|---|
| `TestServeLoginBackground_RejectsSymlink` | ambiente Windows (symlink) |
| `TestApp_ServeMedia_RejectsSymlink` | ambiente Windows (symlink) |
| `TestUpdateContactChatbotMessage_SetsTimestampAndResetsReminder` | pré-existente |

Primeira rodada: além dessas, `TestOccurrenceStages_ReorderConcurrent` falhou — **regressão real minha, corrigida** (bloqueio da linha da organização antes de ler as etapas; 5/5 depois). O pacote `knowledge` não compilou nessa rodada porque o código foi editado durante a execução (artefato; a rodada final compilou tudo). Frontend: `vue-tsc` limpo; vitest 58 arquivos, 458 testes.

## 2. L1 — Encaminhar e Devolver

Quatro operações distintas: **encaminhar** (move a responsabilidade principal, motivo obrigatório), **devolver** (volta ao departamento anterior ou ao solicitante, motivo obrigatório), **apoio** e **acompanhando** (participantes; não movem responsabilidade, não dão WhatsApp nem anexo restrito). Mesmo protocolo; anexos, discussão e histórico ficam. Registro em `occurrence_transfers` e na linha do tempo. Guarda atômica contra movimentos simultâneos; sem ciclo (não se devolve duas vezes seguidas). Quem encaminha continua acompanhando (comentar). O acesso da equipe anterior cai sozinho (é derivado de equipe/responsável). Nenhum `view_all` concedido. Notificação ao cliente **não** é reenviada na movimentação.

Testes: `TestTransfer_*` (4) — passam.

## 3. Os 12 cenários cruzados (perfis reais, ambos os sentidos)

Cada cenário executa 17 passos (criar/encaminhar, receber, notificação em tempo real, só informação autorizada, interação, atualização à origem, apoio, discussão interna, mesma discussão no Chat Interno central, anexos PDF/imagem, visualização inline + antimalware, etapa/prioridade/responsável, devolver e reencaminhar, encerrar/reabrir, PDF, linha do tempo, datas pt-BR).

| # | Cenário | Automatizado (Go, handlers) | Sessões independentes, servidor real | Navegador | Situação |
|---|---|---|---|---|---|
| 1a | SAC → Vendas | APROVADO | não executado | NÃO EXECUTADO | PARCIAL |
| 1b | Vendas → SAC | APROVADO | não executado | NÃO EXECUTADO | PARCIAL |
| 2a | SAC → Logística | APROVADO | APROVADO (22/22) | APROVADO (Encaminhar como Logística) | **APROVADO** |
| 2b | Logística → SAC | APROVADO | não executado | NÃO EXECUTADO | PARCIAL |
| 3a | Sup. Loja → Logística | APROVADO | não executado | NÃO EXECUTADO | PARCIAL |
| 3b | Logística → Sup. Loja | APROVADO | não executado | NÃO EXECUTADO | PARCIAL |
| 4a | Sup. Loja → SAC | APROVADO | não executado | NÃO EXECUTADO | PARCIAL |
| 4b | SAC → Sup. Loja | APROVADO | não executado | NÃO EXECUTADO | PARCIAL |
| 5a | Sup. Loja → Vendas | APROVADO | não executado | NÃO EXECUTADO | PARCIAL |
| 5b | Vendas → Sup. Loja | APROVADO | não executado | NÃO EXECUTADO | PARCIAL |
| 6a | Sup. SAC → SAC | APROVADO | não executado | NÃO EXECUTADO | PARCIAL |
| 6b | SAC → Sup. SAC | APROVADO | não executado | NÃO EXECUTADO | PARCIAL |

O ciclo de 22 verificações em sessões independentes (SAC, Logística, Vendas, Sup. Loja, Sup. SAC, Logística de outra loja) cobriu: SAC não encaminha (403); Logística remetente comenta mas não muda etapa nem puxa de volta (403); outra loja sem acesso a caso, anexos e discussão; Vendas assume, muda etapa, escreve na discussão; SAC lê a discussão; Sup. Loja e Sup. SAC enxergam o caso; Sup. SAC sem conversa de WhatsApp (`restricted`); devolver sem motivo → 400, com motivo → 200, de novo → 403/409; PDF 200. Evidência do navegador: Encaminhar (Logística → Vendas) e Devolver (Vendas → Logística Porto Seguro, com o destino anterior mostrado no diálogo e motivo exigido), linha do tempo atualizada, versão celular.

## 4. Três departamentos num caso

SAC pede, Logística trabalha, apoio de Vendas (escolhido pelo supervisor), supervisão da loja acompanha. Todos veem a discussão completa (4 mensagens); Vendas como apoio **não** lê WhatsApp nem arquivo restrito; o PDF lista os três departamentos e as pessoas. **APROVADO (automatizado)**; navegador: NÃO EXECUTADO.

## 5. Testes negativos

APROVADO (automatizado): outra loja, outra organização, Vendas da mesma loja sem vínculo, apoio sem vínculo — nada de caso, lista, arquivos, download direto (inclusive inline), WhatsApp, escrita, discussão, exportação, auto-adição, opções de apoio, exclusão. Sem `chat:read/write` mesmo estando na equipe: sem WhatsApp, sem envio, sem exportar a conversa. Sem `occurrences:write`: não edita nem muda etapa; ninguém dos perfis apaga protocolo. Apoio de departamento não autorizado → 403. Participar da discussão não dá WhatsApp nem anexo restrito. WhatsApp simulado (provedor falso, origem SAC e Vendas): aviso ao cliente sai uma vez pelo número da Logística; nota, discussão, assumir e etapa não enviam nada; resposta externa só por quem trabalha o caso com `chat:write`; recusas não enviam; histórico preservado.

## 6. Concorrência

APROVADO (automatizado): dois assumem o mesmo caso (um vence); 30 mensagens simultâneas sem duplicar nem perder e contador de não lidas correto; duas mudanças de etapa e duas edições simultâneas sem erro 5xx e sem mistura; mesma redistribuição simultânea; mesmo apoio adicionado duas vezes (um vínculo); dois encaminhamentos simultâneos (um vence, um registro). Tempo real chega só a quem tem acesso (teste WS). **Dois usuários em navegadores simultâneos: NÃO EXECUTADO.**

## 7. Falhas encontradas e correções

1. Reordenação simultânea de etapas passava pela checagem de ordem (corrida de leitura) — corrigido.
2. Apoio adicionado sem permissões ficava sem poder comentar — padrão agora é comentar (lista vazia explícita = somente leitura).
3. Tabela `occurrence_transfers` precisou entrar na migração dos testes.
4. Suposições de teste corrigidas (não eram bugs do produto): GET de WhatsApp responde 200 `restricted` (tratado como negado); o Supervisor de SAC vê todos os protocolos por `occurrences:view_all`; caso aberto por quem já está na equipe destino fica atribuído a quem abriu e o destino o recebe por redistribuição; o novo responsável lê os arquivos do caso (quem "trabalha o caso").
5. Linha do tempo não atualizava após a movimentação na tela — corrigido.
6. Datas: eixos dos gráficos de analytics e prévia do fluxo agora seguem o padrão pt-BR.

## 8. Deriva de permissões do administrador

Causa: a sincronização dos papéis de sistema só liga permissões a papéis **sem nenhuma**, para não sobrescrever personalização; o papel `admin` do banco de dev (criado em 15/09) nunca recebeu `occurrences:delete`, `sales_opportunities:delete`, `sales_opportunities:view_team` e `analytics.agents:view_team`, acrescentadas depois. Nada foi concedido automaticamente. Antes de qualquer deploy, conferir os papéis de produção e decidir uma migração aditiva revisada.

## 9. Pendências por gravidade

- **Alta (bloqueia aprovação de merge):** homologação no navegador das 11 combinações restantes e das visões de SAC solicitante e supervisores; teste simultâneo com dois usuários em navegadores.
- **Média:** decisão sobre a migração de permissões dos papéis de sistema antes de deploy; cenários legados (casos encaminhados antes do registro de movimentações usam o evento "encaminhado" como origem).
- **Baixa:** gráficos de analytics de outras telas além dos dois corrigidos; filtro "Departamento" do PDF depende do cadastro do usuário; o PDF não afirma autenticidade (código só ligado ao log de auditoria).

## 10. Decisão técnica sobre prontidão para merge

**Não pronta para avaliação de merge.** Código, testes automatizados e regressão estão consistentes e sem regressões abertas, e o ciclo operacional completo foi comprovado em servidor real com sessões independentes e no navegador para uma combinação. A exigência de homologação em navegador para as demais combinações e o teste simultâneo em navegadores continuam **NÃO EXECUTADOS**, e a Etapa 7 só se encerra com eles.
