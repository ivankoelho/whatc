# Homologação cruzada entre departamentos — feature/team-role-profiles

Data: 26/10/2026. Matriz de perfis: `00-matriz-perfis-permissoes.md`. Proposta de migração de permissões (não executada): `02-proposta-migracao-permissoes.md`.

## 1. Método e limites (leia antes dos resultados)

- **Servidor real** (`:8081`, banco `whatomate_valida`, ClamAV real em contêiner), **uma sessão independente por usuário** (cookies próprios) e **um cliente WebSocket por sessão**, com os usuários de teste e as **permissões efetivas dos papéis** (nada foi concedido para passar). Evidência bruta: `hom_final.json` (468 verificações), `hom2.json` (concorrência) e `hom3.json` (negativos) no diretório de trabalho da sessão.
- **Navegador:** o painel permite um navegador (uma pessoa por vez). Evidência no navegador: (a) prévia de PDF e imagem com cada um dos 5 perfis receptores, trocando de sessão; (b) Encaminhar e Devolver na tela; (c) um teste simultâneo com a tela de um perfil aberta enquanto duas outras sessões independentes agiam no mesmo protocolo; (d) perda de acesso refletida na tela. **Não houve seis navegadores abertos em paralelo**; as demais sessões foram HTTP/WebSocket reais.
- **ClamAV:** o veredito *clean* é do ClamAV real. O veredito *infected* real não foi obtido: o Windows Defender do host remove o arquivo EICAR antes da leitura (`cannot open file` → estado `error`); o arquivo fica sem veredito limpo e o download é bloqueado (423). O veredito *infected* está coberto pelos testes Go (scanner falso) e pela homologação anterior.
- **Observação de perfil:** um colega com perfil Supervisor de SAC é supervisor por `occurrences:view_all`; no cenário com destino Supervisor de SAC o apoio usado foi um colega de SAC.

## 2. Resumo

| | Quantidade |
|---|---|
| Cenários cruzados **APROVADOS** | **12 de 12** (11 combinações obrigatórias nos dois sentidos + 1 repetição SAC → Logística; o ciclo completo SAC → Logística → Vendas já tinha evidência de navegador da rodada anterior) |
| Cenários **REPROVADOS** | 0 |
| Cenários **NÃO EXECUTADOS** | 0 |
| Verificações dos cenários | 468 de 468 |
| Teste simultâneo | 18 de 18 (API/WebSocket reais) + evidência ao vivo no navegador |
| Testes negativos | 13 de 13 (API) + perda de acesso no navegador |
| Cenário de três departamentos | APROVADO (Go, nível de handler; ver seção 6) |

Observação sobre "APROVADO": cada cenário passou em **todas** as 12 verificações obrigatórias no servidor real; a parte que exige olhar a tela (prévia, tela atualizada ao vivo, perda de acesso) foi comprovada no navegador **por perfil receptor** e no teste simultâneo, e não repetida em tela para cada uma das 12 combinações.

## 3. Cenários (12) — passos, esperado, observado e situação

### SAC para Vendas

- **Perfis/usuários:** origem sac@perfil.local (Teste SAC); destino vendas@perfil.local (Teste Vendas); colega de apoio vendas.colega@perfil.local; outsiders: logistica2@perfil.local (outra loja), estrangeiro@perfil.local (outra organização).
- **Protocolo:** 2026-000295
- **Passos:** 39 verificações, 0 reprovadas. **Situação: APROVADO** (servidor real, sessões independentes, ClamAV real; prévia em navegador por perfil receptor, ver seção 4).

| Item | Passo | Esperado | Observado | Resultado |
|---|---|---|---|---|
| 1 Abertura e recebimento | origem abre o caso para a equipe de destino | 200 | 200 | APROVADO |
| 1 Abertura e recebimento | destino recebe e vê na lista | 200 + listado | 200 true | APROVADO |
| 1 Abertura e recebimento | origem continua vendo | 200 | 200 | APROVADO |
| 2 Encaminhamento e devolução | devolver sem motivo | 400 | 400 | APROVADO |
| 2 Encaminhamento e devolução | destino devolve com motivo | 200 e equipe volta à origem | 200 equipe="" | APROVADO |
| 2 Encaminhamento e devolução | devolver de novo (sem ciclo) | 403 ou 409 | 403 | APROVADO |
| 2 Encaminhamento e devolução | origem reencaminha ao destino (mesmo protocolo) | 200 e equipe = destino | 200 equipe="Vendas Porto Seguro" protocolo=2026-000295 | APROVADO |
| 2 Encaminhamento e devolução | encaminhar ao mesmo destino de novo | 409 ou 403 | 403 | APROVADO |
| 3 Responsável e equipe | destino assume o caso | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino muda a etapa | 200 | 200 | APROVADO |
| 3 Responsável e equipe | supervisor altera o responsável para a pessoa do destino | 200 e responsável trocado | 200 Colega Teste Vendas | APROVADO |
| 3 Responsável e equipe | novo responsável (não supervisor) devolve a titularidade ao colega | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino altera a prioridade | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | adiciona pessoa de apoio do departamento | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio abre o caso e comenta | 200/200 | 200/200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio sem WhatsApp | restricted=true | true | APROVADO |
| 6 Apoio (incluir/remover) | apoio não encaminha nem redistribui | 403/403 | 403/403 | APROVADO |
| 6 Apoio (incluir/remover) | apoio não abre outro protocolo | 403/404 | 403 | APROVADO |
| 4 Interações | interações do destino e da origem registradas | 200/200 e visíveis para ambos | 200/200 | APROVADO |
| 5 Discussão interna compartilhada | mesmo canal para os três e mesmas mensagens, sem duplicar | mesmo canal; 3 msgs iguais | mesmoCanal=true msgs=3 | APROVADO |
| 5 Discussão interna compartilhada | outra loja e outra organização não leem a discussão | 403/404 | 404/404 | APROVADO |
| 5 Discussão interna compartilhada | a mesma discussão aparece no Chat Interno central | canal listado | true | APROVADO |
| 7 PDF e imagem (prévia inline) | anexa PDF (restrito) e PNG (aberto) com veredito limpo | 200 e scan clean | 200/200 clean/clean | APROVADO |
| 7 PDF e imagem (prévia inline) | prévia inline do PDF (tipo pelos bytes, CSP sandbox, nosniff) | 200 application/pdf inline | 200 application/pdf/inline; filename*=UTF-8''laudo.pdf/true/nosniff | APROVADO |
| 7 PDF e imagem (prévia inline) | apoio vê a imagem aberta inline e não o PDF restrito | 200 image/png / 403 | 200 image/png / 403 | APROVADO |
| 7 PDF e imagem (prévia inline) | origem que só acompanha não lê o PDF restrito | 403 | 403 | APROVADO |
| 8 Bloqueio sem veredito limpo | arquivo com assinatura EICAR (ClamAV real) não fica liberado | não clean e download bloqueado | 200 scan=error download=423 | APROVADO |
| 6 Apoio (incluir/remover) | remove a pessoa de apoio | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio removido perde o acesso | 403/404 | 403/403 | APROVADO |
| 9 Tempo real | destino recebeu atualizações em tempo real do caso | >0 eventos | 18 | APROVADO |
| 9 Tempo real | origem recebeu atualizações em tempo real | >0 eventos | 18 | APROVADO |
| 9 Tempo real | outra loja e outra organização não receberam nada | 0/0 | 0/0 | APROVADO |
| 10 Timeline | linha do tempo completa (abertura, encaminhamento, devolução, responsável, etapa, notas, apoio, arquivos) | todos os tipos | completa | APROVADO |
| 11 Relatório PDF | relatório PDF do caso (partes, histórico, anexos, emissão, datas pt-BR) | 200 + conteúdo | 200 7178 chars | APROVADO |
| 11 Relatório PDF | origem não exporta a conversa do cliente | 403 | 403 | APROVADO |
| 12 Restrições de acesso | outra loja e outra organização: caso, anexos (inclusive URL direta e inline), WhatsApp, escrita, encaminhar, reassign, exportar, participantes | tudo 403/404 | [["outra loja",[403,403,403,403,403,403,403,403,403,403]],["outra organização",[404,404,404,404,404,404,404,40 | APROVADO |
| 12 Restrições de acesso | WhatsApp de outra loja não devolve mensagens | 403/404 ou restricted | 403 undefined | APROVADO |
| 12 Restrições de acesso | quem apenas acompanha não lê o WhatsApp | restricted=true | true | APROVADO |
| 12 Restrições de acesso | solicitante que só acompanha não encaminha nem altera o responsável | 403/403 | 403/403 | APROVADO |

### Vendas para SAC

- **Perfis/usuários:** origem vendas@perfil.local (Teste Vendas); destino sac@perfil.local (Teste SAC); colega de apoio sac.colega@perfil.local; outsiders: logistica2@perfil.local (outra loja), estrangeiro@perfil.local (outra organização).
- **Protocolo:** 2026-000297
- **Passos:** 39 verificações, 0 reprovadas. **Situação: APROVADO** (servidor real, sessões independentes, ClamAV real; prévia em navegador por perfil receptor, ver seção 4).

| Item | Passo | Esperado | Observado | Resultado |
|---|---|---|---|---|
| 1 Abertura e recebimento | origem abre o caso para a equipe de destino | 200 | 200 | APROVADO |
| 1 Abertura e recebimento | destino recebe e vê na lista | 200 + listado | 200 true | APROVADO |
| 1 Abertura e recebimento | origem continua vendo | 200 | 200 | APROVADO |
| 2 Encaminhamento e devolução | devolver sem motivo | 400 | 400 | APROVADO |
| 2 Encaminhamento e devolução | destino devolve com motivo | 200 e equipe volta à origem | 200 equipe="" | APROVADO |
| 2 Encaminhamento e devolução | devolver de novo (sem ciclo) | 403 ou 409 | 403 | APROVADO |
| 2 Encaminhamento e devolução | origem reencaminha ao destino (mesmo protocolo) | 200 e equipe = destino | 200 equipe="SAC Central" protocolo=2026-000297 | APROVADO |
| 2 Encaminhamento e devolução | encaminhar ao mesmo destino de novo | 409 ou 403 | 403 | APROVADO |
| 3 Responsável e equipe | destino assume o caso | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino muda a etapa | 200 | 200 | APROVADO |
| 3 Responsável e equipe | supervisor altera o responsável para a pessoa do destino | 200 e responsável trocado | 200 Colega Teste SAC | APROVADO |
| 3 Responsável e equipe | novo responsável (não supervisor) devolve a titularidade ao colega | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino altera a prioridade | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | adiciona pessoa de apoio do departamento | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio abre o caso e comenta | 200/200 | 200/200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio sem WhatsApp | restricted=true | true | APROVADO |
| 6 Apoio (incluir/remover) | apoio não encaminha nem redistribui | 403/403 | 403/403 | APROVADO |
| 6 Apoio (incluir/remover) | apoio não abre outro protocolo | 403/404 | 403 | APROVADO |
| 4 Interações | interações do destino e da origem registradas | 200/200 e visíveis para ambos | 200/200 | APROVADO |
| 5 Discussão interna compartilhada | mesmo canal para os três e mesmas mensagens, sem duplicar | mesmo canal; 3 msgs iguais | mesmoCanal=true msgs=3 | APROVADO |
| 5 Discussão interna compartilhada | outra loja e outra organização não leem a discussão | 403/404 | 404/404 | APROVADO |
| 5 Discussão interna compartilhada | a mesma discussão aparece no Chat Interno central | canal listado | true | APROVADO |
| 7 PDF e imagem (prévia inline) | anexa PDF (restrito) e PNG (aberto) com veredito limpo | 200 e scan clean | 200/200 clean/clean | APROVADO |
| 7 PDF e imagem (prévia inline) | prévia inline do PDF (tipo pelos bytes, CSP sandbox, nosniff) | 200 application/pdf inline | 200 application/pdf/inline; filename*=UTF-8''laudo.pdf/true/nosniff | APROVADO |
| 7 PDF e imagem (prévia inline) | apoio vê a imagem aberta inline e não o PDF restrito | 200 image/png / 403 | 200 image/png / 403 | APROVADO |
| 7 PDF e imagem (prévia inline) | origem que só acompanha não lê o PDF restrito | 403 | 403 | APROVADO |
| 8 Bloqueio sem veredito limpo | arquivo com assinatura EICAR (ClamAV real) não fica liberado | não clean e download bloqueado | 200 scan=error download=423 | APROVADO |
| 6 Apoio (incluir/remover) | remove a pessoa de apoio | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio removido perde o acesso | 403/404 | 403/403 | APROVADO |
| 9 Tempo real | destino recebeu atualizações em tempo real do caso | >0 eventos | 18 | APROVADO |
| 9 Tempo real | origem recebeu atualizações em tempo real | >0 eventos | 18 | APROVADO |
| 9 Tempo real | outra loja e outra organização não receberam nada | 0/0 | 0/0 | APROVADO |
| 10 Timeline | linha do tempo completa (abertura, encaminhamento, devolução, responsável, etapa, notas, apoio, arquivos) | todos os tipos | completa | APROVADO |
| 11 Relatório PDF | relatório PDF do caso (partes, histórico, anexos, emissão, datas pt-BR) | 200 + conteúdo | 200 7189 chars | APROVADO |
| 11 Relatório PDF | origem não exporta a conversa do cliente | 403 | 403 | APROVADO |
| 12 Restrições de acesso | outra loja e outra organização: caso, anexos (inclusive URL direta e inline), WhatsApp, escrita, encaminhar, reassign, exportar, participantes | tudo 403/404 | [["outra loja",[403,403,403,403,403,403,403,403,403,403]],["outra organização",[404,404,404,404,404,404,404,40 | APROVADO |
| 12 Restrições de acesso | WhatsApp de outra loja não devolve mensagens | 403/404 ou restricted | 403 undefined | APROVADO |
| 12 Restrições de acesso | quem apenas acompanha não lê o WhatsApp | restricted=true | true | APROVADO |
| 12 Restrições de acesso | solicitante que só acompanha não encaminha nem altera o responsável | 403/403 | 403/403 | APROVADO |

### Logística para SAC

- **Perfis/usuários:** origem logistica@perfil.local (Teste Logística); destino sac@perfil.local (Teste SAC); colega de apoio sac.colega@perfil.local; outsiders: logistica2@perfil.local (outra loja), estrangeiro@perfil.local (outra organização).
- **Protocolo:** 2026-000299
- **Passos:** 39 verificações, 0 reprovadas. **Situação: APROVADO** (servidor real, sessões independentes, ClamAV real; prévia em navegador por perfil receptor, ver seção 4).

| Item | Passo | Esperado | Observado | Resultado |
|---|---|---|---|---|
| 1 Abertura e recebimento | origem abre o caso para a equipe de destino | 200 | 200 | APROVADO |
| 1 Abertura e recebimento | destino recebe e vê na lista | 200 + listado | 200 true | APROVADO |
| 1 Abertura e recebimento | origem continua vendo | 200 | 200 | APROVADO |
| 2 Encaminhamento e devolução | devolver sem motivo | 400 | 400 | APROVADO |
| 2 Encaminhamento e devolução | destino devolve com motivo | 200 e equipe volta à origem | 200 equipe="" | APROVADO |
| 2 Encaminhamento e devolução | devolver de novo (sem ciclo) | 403 ou 409 | 403 | APROVADO |
| 2 Encaminhamento e devolução | origem reencaminha ao destino (mesmo protocolo) | 200 e equipe = destino | 200 equipe="SAC Central" protocolo=2026-000299 | APROVADO |
| 2 Encaminhamento e devolução | encaminhar ao mesmo destino de novo | 409 ou 403 | 403 | APROVADO |
| 3 Responsável e equipe | destino assume o caso | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino muda a etapa | 200 | 200 | APROVADO |
| 3 Responsável e equipe | supervisor altera o responsável para a pessoa do destino | 200 e responsável trocado | 200 Colega Teste SAC | APROVADO |
| 3 Responsável e equipe | novo responsável (não supervisor) devolve a titularidade ao colega | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino altera a prioridade | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | adiciona pessoa de apoio do departamento | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio abre o caso e comenta | 200/200 | 200/200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio sem WhatsApp | restricted=true | true | APROVADO |
| 6 Apoio (incluir/remover) | apoio não encaminha nem redistribui | 403/403 | 403/403 | APROVADO |
| 6 Apoio (incluir/remover) | apoio não abre outro protocolo | 403/404 | 403 | APROVADO |
| 4 Interações | interações do destino e da origem registradas | 200/200 e visíveis para ambos | 200/200 | APROVADO |
| 5 Discussão interna compartilhada | mesmo canal para os três e mesmas mensagens, sem duplicar | mesmo canal; 3 msgs iguais | mesmoCanal=true msgs=3 | APROVADO |
| 5 Discussão interna compartilhada | outra loja e outra organização não leem a discussão | 403/404 | 404/404 | APROVADO |
| 5 Discussão interna compartilhada | a mesma discussão aparece no Chat Interno central | canal listado | true | APROVADO |
| 7 PDF e imagem (prévia inline) | anexa PDF (restrito) e PNG (aberto) com veredito limpo | 200 e scan clean | 200/200 clean/clean | APROVADO |
| 7 PDF e imagem (prévia inline) | prévia inline do PDF (tipo pelos bytes, CSP sandbox, nosniff) | 200 application/pdf inline | 200 application/pdf/inline; filename*=UTF-8''laudo.pdf/true/nosniff | APROVADO |
| 7 PDF e imagem (prévia inline) | apoio vê a imagem aberta inline e não o PDF restrito | 200 image/png / 403 | 200 image/png / 403 | APROVADO |
| 7 PDF e imagem (prévia inline) | origem que só acompanha não lê o PDF restrito | 403 | 403 | APROVADO |
| 8 Bloqueio sem veredito limpo | arquivo com assinatura EICAR (ClamAV real) não fica liberado | não clean e download bloqueado | 200 scan=error download=423 | APROVADO |
| 6 Apoio (incluir/remover) | remove a pessoa de apoio | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio removido perde o acesso | 403/404 | 403/403 | APROVADO |
| 9 Tempo real | destino recebeu atualizações em tempo real do caso | >0 eventos | 18 | APROVADO |
| 9 Tempo real | origem recebeu atualizações em tempo real | >0 eventos | 18 | APROVADO |
| 9 Tempo real | outra loja e outra organização não receberam nada | 0/0 | 0/0 | APROVADO |
| 10 Timeline | linha do tempo completa (abertura, encaminhamento, devolução, responsável, etapa, notas, apoio, arquivos) | todos os tipos | completa | APROVADO |
| 11 Relatório PDF | relatório PDF do caso (partes, histórico, anexos, emissão, datas pt-BR) | 200 + conteúdo | 200 7256 chars | APROVADO |
| 11 Relatório PDF | origem não exporta a conversa do cliente | 403 | 403 | APROVADO |
| 12 Restrições de acesso | outra loja e outra organização: caso, anexos (inclusive URL direta e inline), WhatsApp, escrita, encaminhar, reassign, exportar, participantes | tudo 403/404 | [["outra loja",[403,403,403,403,403,403,403,403,403,403]],["outra organização",[404,404,404,404,404,404,404,40 | APROVADO |
| 12 Restrições de acesso | WhatsApp de outra loja não devolve mensagens | 403/404 ou restricted | 403 undefined | APROVADO |
| 12 Restrições de acesso | quem apenas acompanha não lê o WhatsApp | restricted=true | true | APROVADO |
| 12 Restrições de acesso | solicitante que só acompanha não encaminha nem altera o responsável | 403/403 | 403/403 | APROVADO |

### Supervisor de Loja para Logística

- **Perfis/usuários:** origem suploja@perfil.local (Teste Supervisor de Loja); destino logistica@perfil.local (Teste Logística); colega de apoio logistica.colega@perfil.local; outsiders: logistica2@perfil.local (outra loja), estrangeiro@perfil.local (outra organização).
- **Protocolo:** 2026-000301
- **Passos:** 39 verificações, 0 reprovadas. **Situação: APROVADO** (servidor real, sessões independentes, ClamAV real; prévia em navegador por perfil receptor, ver seção 4).

| Item | Passo | Esperado | Observado | Resultado |
|---|---|---|---|---|
| 1 Abertura e recebimento | origem abre o caso para a equipe de destino | 200 | 200 | APROVADO |
| 1 Abertura e recebimento | destino recebe e vê na lista | 200 + listado | 200 true | APROVADO |
| 1 Abertura e recebimento | origem continua vendo | 200 | 200 | APROVADO |
| 2 Encaminhamento e devolução | devolver sem motivo | 400 | 400 | APROVADO |
| 2 Encaminhamento e devolução | destino devolve com motivo | 200 e equipe volta à origem | 200 equipe="" | APROVADO |
| 2 Encaminhamento e devolução | devolver de novo (sem ciclo) | 403 ou 409 | 403 | APROVADO |
| 2 Encaminhamento e devolução | origem reencaminha ao destino (mesmo protocolo) | 200 e equipe = destino | 200 equipe="Logistica Porto Seguro" protocolo=2026-000301 | APROVADO |
| 2 Encaminhamento e devolução | encaminhar ao mesmo destino de novo | 409 ou 403 | 409 | APROVADO |
| 3 Responsável e equipe | destino assume o caso | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino muda a etapa | 200 | 200 | APROVADO |
| 3 Responsável e equipe | supervisor altera o responsável para a pessoa do destino | 200 e responsável trocado | 200 Colega Teste Log�stica | APROVADO |
| 3 Responsável e equipe | novo responsável (não supervisor) devolve a titularidade ao colega | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino altera a prioridade | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | adiciona pessoa de apoio do departamento | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio abre o caso e comenta | 200/200 | 200/200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio sem WhatsApp | restricted=true | true | APROVADO |
| 6 Apoio (incluir/remover) | apoio não encaminha nem redistribui | 403/403 | 403/403 | APROVADO |
| 6 Apoio (incluir/remover) | apoio não abre outro protocolo | 403/404 | 403 | APROVADO |
| 4 Interações | interações do destino e da origem registradas | 200/200 e visíveis para ambos | 200/200 | APROVADO |
| 5 Discussão interna compartilhada | mesmo canal para os três e mesmas mensagens, sem duplicar | mesmo canal; 3 msgs iguais | mesmoCanal=true msgs=3 | APROVADO |
| 5 Discussão interna compartilhada | outra loja e outra organização não leem a discussão | 403/404 | 404/404 | APROVADO |
| 5 Discussão interna compartilhada | a mesma discussão aparece no Chat Interno central | canal listado | true | APROVADO |
| 7 PDF e imagem (prévia inline) | anexa PDF (restrito) e PNG (aberto) com veredito limpo | 200 e scan clean | 200/200 clean/clean | APROVADO |
| 7 PDF e imagem (prévia inline) | prévia inline do PDF (tipo pelos bytes, CSP sandbox, nosniff) | 200 application/pdf inline | 200 application/pdf/inline; filename*=UTF-8''laudo.pdf/true/nosniff | APROVADO |
| 7 PDF e imagem (prévia inline) | apoio vê a imagem aberta inline e não o PDF restrito | 200 image/png / 403 | 200 image/png / 403 | APROVADO |
| 7 PDF e imagem (prévia inline) | origem que só acompanha não lê o PDF restrito | conforme perfil | 200 | APROVADO |
| 8 Bloqueio sem veredito limpo | arquivo com assinatura EICAR (ClamAV real) não fica liberado | não clean e download bloqueado | 200 scan=error download=423 | APROVADO |
| 6 Apoio (incluir/remover) | remove a pessoa de apoio | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio removido perde o acesso | 403/404 | 403/403 | APROVADO |
| 9 Tempo real | destino recebeu atualizações em tempo real do caso | >0 eventos | 18 | APROVADO |
| 9 Tempo real | origem recebeu atualizações em tempo real | >0 eventos | 18 | APROVADO |
| 9 Tempo real | outra loja e outra organização não receberam nada | 0/0 | 0/0 | APROVADO |
| 10 Timeline | linha do tempo completa (abertura, encaminhamento, devolução, responsável, etapa, notas, apoio, arquivos) | todos os tipos | completa | APROVADO |
| 11 Relatório PDF | relatório PDF do caso (partes, histórico, anexos, emissão, datas pt-BR) | 200 + conteúdo | 200 7493 chars | APROVADO |
| 11 Relatório PDF | origem não exporta a conversa do cliente | conforme perfil | 200 | APROVADO |
| 12 Restrições de acesso | outra loja e outra organização: caso, anexos (inclusive URL direta e inline), WhatsApp, escrita, encaminhar, reassign, exportar, participantes | tudo 403/404 | [["outra loja",[403,403,403,403,403,403,403,403,403,403]],["outra organização",[404,404,404,404,404,404,404,40 | APROVADO |
| 12 Restrições de acesso | WhatsApp de outra loja não devolve mensagens | 403/404 ou restricted | 403 undefined | APROVADO |
| 12 Restrições de acesso | quem apenas acompanha não lê o WhatsApp | conforme perfil | false | APROVADO |
| 12 Restrições de acesso | solicitante que só acompanha não encaminha nem altera o responsável | conforme perfil | 200/200 | APROVADO |

### Logística para Supervisor de Loja

- **Perfis/usuários:** origem logistica@perfil.local (Teste Logística); destino suploja@perfil.local (Teste Supervisor de Loja); colega de apoio suploja.colega@perfil.local; outsiders: logistica2@perfil.local (outra loja), estrangeiro@perfil.local (outra organização).
- **Protocolo:** 2026-000303
- **Passos:** 39 verificações, 0 reprovadas. **Situação: APROVADO** (servidor real, sessões independentes, ClamAV real; prévia em navegador por perfil receptor, ver seção 4).

| Item | Passo | Esperado | Observado | Resultado |
|---|---|---|---|---|
| 1 Abertura e recebimento | origem abre o caso para a equipe de destino | 200 | 200 | APROVADO |
| 1 Abertura e recebimento | destino recebe e vê na lista | 200 + listado | 200 true | APROVADO |
| 1 Abertura e recebimento | origem continua vendo | 200 | 200 | APROVADO |
| 2 Encaminhamento e devolução | devolver sem motivo | 400 | 400 | APROVADO |
| 2 Encaminhamento e devolução | destino devolve com motivo | 200 e equipe volta à origem | 200 equipe="" | APROVADO |
| 2 Encaminhamento e devolução | devolver de novo (sem ciclo) | 403 ou 409 | 409 | APROVADO |
| 2 Encaminhamento e devolução | origem reencaminha ao destino (mesmo protocolo) | 200 e equipe = destino | 200 equipe="Supervisão Porto Seguro" protocolo=2026-000303 | APROVADO |
| 2 Encaminhamento e devolução | encaminhar ao mesmo destino de novo | 409 ou 403 | 403 | APROVADO |
| 3 Responsável e equipe | destino assume o caso | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino muda a etapa | 200 | 200 | APROVADO |
| 3 Responsável e equipe | supervisor altera o responsável para a pessoa do destino | 200 e responsável trocado | 200 Colega Teste Supervisor de Loja | APROVADO |
| 3 Responsável e equipe | novo responsável (não supervisor) devolve a titularidade ao colega | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino altera a prioridade | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | adiciona pessoa de apoio do departamento | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio abre o caso e comenta | 200/200 | 200/200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio sem WhatsApp | restricted=true | true | APROVADO |
| 6 Apoio (incluir/remover) | apoio não encaminha nem redistribui | 403/403 | 403/403 | APROVADO |
| 6 Apoio (incluir/remover) | apoio não abre outro protocolo | 403/404 | 403 | APROVADO |
| 4 Interações | interações do destino e da origem registradas | 200/200 e visíveis para ambos | 200/200 | APROVADO |
| 5 Discussão interna compartilhada | mesmo canal para os três e mesmas mensagens, sem duplicar | mesmo canal; 3 msgs iguais | mesmoCanal=true msgs=3 | APROVADO |
| 5 Discussão interna compartilhada | outra loja e outra organização não leem a discussão | 403/404 | 404/404 | APROVADO |
| 5 Discussão interna compartilhada | a mesma discussão aparece no Chat Interno central | canal listado | true | APROVADO |
| 7 PDF e imagem (prévia inline) | anexa PDF (restrito) e PNG (aberto) com veredito limpo | 200 e scan clean | 200/200 clean/clean | APROVADO |
| 7 PDF e imagem (prévia inline) | prévia inline do PDF (tipo pelos bytes, CSP sandbox, nosniff) | 200 application/pdf inline | 200 application/pdf/inline; filename*=UTF-8''laudo.pdf/true/nosniff | APROVADO |
| 7 PDF e imagem (prévia inline) | apoio vê a imagem aberta inline e não o PDF restrito | 200 image/png / 403 | 200 image/png / 403 | APROVADO |
| 7 PDF e imagem (prévia inline) | origem que só acompanha não lê o PDF restrito | 403 | 403 | APROVADO |
| 8 Bloqueio sem veredito limpo | arquivo com assinatura EICAR (ClamAV real) não fica liberado | não clean e download bloqueado | 200 scan=error download=423 | APROVADO |
| 6 Apoio (incluir/remover) | remove a pessoa de apoio | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio removido perde o acesso | 403/404 | 403/403 | APROVADO |
| 9 Tempo real | destino recebeu atualizações em tempo real do caso | >0 eventos | 18 | APROVADO |
| 9 Tempo real | origem recebeu atualizações em tempo real | >0 eventos | 18 | APROVADO |
| 9 Tempo real | outra loja e outra organização não receberam nada | 0/0 | 0/0 | APROVADO |
| 10 Timeline | linha do tempo completa (abertura, encaminhamento, devolução, responsável, etapa, notas, apoio, arquivos) | todos os tipos | completa | APROVADO |
| 11 Relatório PDF | relatório PDF do caso (partes, histórico, anexos, emissão, datas pt-BR) | 200 + conteúdo | 200 7559 chars | APROVADO |
| 11 Relatório PDF | origem não exporta a conversa do cliente | 403 | 403 | APROVADO |
| 12 Restrições de acesso | outra loja e outra organização: caso, anexos (inclusive URL direta e inline), WhatsApp, escrita, encaminhar, reassign, exportar, participantes | tudo 403/404 | [["outra loja",[403,403,403,403,403,403,403,403,403,403]],["outra organização",[404,404,404,404,404,404,404,40 | APROVADO |
| 12 Restrições de acesso | WhatsApp de outra loja não devolve mensagens | 403/404 ou restricted | 403 undefined | APROVADO |
| 12 Restrições de acesso | quem apenas acompanha não lê o WhatsApp | restricted=true | true | APROVADO |
| 12 Restrições de acesso | solicitante que só acompanha não encaminha nem altera o responsável | 403/403 | 403/403 | APROVADO |

### Supervisor de Loja para SAC

- **Perfis/usuários:** origem suploja@perfil.local (Teste Supervisor de Loja); destino sac@perfil.local (Teste SAC); colega de apoio sac.colega@perfil.local; outsiders: logistica2@perfil.local (outra loja), estrangeiro@perfil.local (outra organização).
- **Protocolo:** 2026-000305
- **Passos:** 39 verificações, 0 reprovadas. **Situação: APROVADO** (servidor real, sessões independentes, ClamAV real; prévia em navegador por perfil receptor, ver seção 4).

| Item | Passo | Esperado | Observado | Resultado |
|---|---|---|---|---|
| 1 Abertura e recebimento | origem abre o caso para a equipe de destino | 200 | 200 | APROVADO |
| 1 Abertura e recebimento | destino recebe e vê na lista | 200 + listado | 200 true | APROVADO |
| 1 Abertura e recebimento | origem continua vendo | 200 | 200 | APROVADO |
| 2 Encaminhamento e devolução | devolver sem motivo | 400 | 400 | APROVADO |
| 2 Encaminhamento e devolução | destino devolve com motivo | 200 e equipe volta à origem | 200 equipe="" | APROVADO |
| 2 Encaminhamento e devolução | devolver de novo (sem ciclo) | 403 ou 409 | 403 | APROVADO |
| 2 Encaminhamento e devolução | origem reencaminha ao destino (mesmo protocolo) | 200 e equipe = destino | 200 equipe="SAC Central" protocolo=2026-000305 | APROVADO |
| 2 Encaminhamento e devolução | encaminhar ao mesmo destino de novo | 409 ou 403 | 403 | APROVADO |
| 3 Responsável e equipe | destino assume o caso | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino muda a etapa | 200 | 200 | APROVADO |
| 3 Responsável e equipe | supervisor altera o responsável para a pessoa do destino | 200 e responsável trocado | 200 Colega Teste SAC | APROVADO |
| 3 Responsável e equipe | novo responsável (não supervisor) devolve a titularidade ao colega | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino altera a prioridade | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | adiciona pessoa de apoio do departamento | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio abre o caso e comenta | 200/200 | 200/200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio sem WhatsApp | restricted=true | true | APROVADO |
| 6 Apoio (incluir/remover) | apoio não encaminha nem redistribui | 403/403 | 403/403 | APROVADO |
| 6 Apoio (incluir/remover) | apoio não abre outro protocolo | 403/404 | 403 | APROVADO |
| 4 Interações | interações do destino e da origem registradas | 200/200 e visíveis para ambos | 200/200 | APROVADO |
| 5 Discussão interna compartilhada | mesmo canal para os três e mesmas mensagens, sem duplicar | mesmo canal; 3 msgs iguais | mesmoCanal=true msgs=3 | APROVADO |
| 5 Discussão interna compartilhada | outra loja e outra organização não leem a discussão | 403/404 | 404/404 | APROVADO |
| 5 Discussão interna compartilhada | a mesma discussão aparece no Chat Interno central | canal listado | true | APROVADO |
| 7 PDF e imagem (prévia inline) | anexa PDF (restrito) e PNG (aberto) com veredito limpo | 200 e scan clean | 200/200 clean/clean | APROVADO |
| 7 PDF e imagem (prévia inline) | prévia inline do PDF (tipo pelos bytes, CSP sandbox, nosniff) | 200 application/pdf inline | 200 application/pdf/inline; filename*=UTF-8''laudo.pdf/true/nosniff | APROVADO |
| 7 PDF e imagem (prévia inline) | apoio vê a imagem aberta inline e não o PDF restrito | 200 image/png / 403 | 200 image/png / 403 | APROVADO |
| 7 PDF e imagem (prévia inline) | origem que só acompanha não lê o PDF restrito | conforme perfil | 403 | APROVADO |
| 8 Bloqueio sem veredito limpo | arquivo com assinatura EICAR (ClamAV real) não fica liberado | não clean e download bloqueado | 200 scan=error download=423 | APROVADO |
| 6 Apoio (incluir/remover) | remove a pessoa de apoio | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio removido perde o acesso | 403/404 | 403/403 | APROVADO |
| 9 Tempo real | destino recebeu atualizações em tempo real do caso | >0 eventos | 18 | APROVADO |
| 9 Tempo real | origem recebeu atualizações em tempo real | >0 eventos | 18 | APROVADO |
| 9 Tempo real | outra loja e outra organização não receberam nada | 0/0 | 0/0 | APROVADO |
| 10 Timeline | linha do tempo completa (abertura, encaminhamento, devolução, responsável, etapa, notas, apoio, arquivos) | todos os tipos | completa | APROVADO |
| 11 Relatório PDF | relatório PDF do caso (partes, histórico, anexos, emissão, datas pt-BR) | 200 + conteúdo | 200 7421 chars | APROVADO |
| 11 Relatório PDF | origem não exporta a conversa do cliente | conforme perfil | 403 | APROVADO |
| 12 Restrições de acesso | outra loja e outra organização: caso, anexos (inclusive URL direta e inline), WhatsApp, escrita, encaminhar, reassign, exportar, participantes | tudo 403/404 | [["outra loja",[403,403,403,403,403,403,403,403,403,403]],["outra organização",[404,404,404,404,404,404,404,40 | APROVADO |
| 12 Restrições de acesso | WhatsApp de outra loja não devolve mensagens | 403/404 ou restricted | 403 undefined | APROVADO |
| 12 Restrições de acesso | quem apenas acompanha não lê o WhatsApp | conforme perfil | true | APROVADO |
| 12 Restrições de acesso | solicitante que só acompanha não encaminha nem altera o responsável | conforme perfil | 403/403 | APROVADO |

### SAC para Supervisor de Loja

- **Perfis/usuários:** origem sac@perfil.local (Teste SAC); destino suploja@perfil.local (Teste Supervisor de Loja); colega de apoio suploja.colega@perfil.local; outsiders: logistica2@perfil.local (outra loja), estrangeiro@perfil.local (outra organização).
- **Protocolo:** 2026-000307
- **Passos:** 39 verificações, 0 reprovadas. **Situação: APROVADO** (servidor real, sessões independentes, ClamAV real; prévia em navegador por perfil receptor, ver seção 4).

| Item | Passo | Esperado | Observado | Resultado |
|---|---|---|---|---|
| 1 Abertura e recebimento | origem abre o caso para a equipe de destino | 200 | 200 | APROVADO |
| 1 Abertura e recebimento | destino recebe e vê na lista | 200 + listado | 200 true | APROVADO |
| 1 Abertura e recebimento | origem continua vendo | 200 | 200 | APROVADO |
| 2 Encaminhamento e devolução | devolver sem motivo | 400 | 400 | APROVADO |
| 2 Encaminhamento e devolução | destino devolve com motivo | 200 e equipe volta à origem | 200 equipe="" | APROVADO |
| 2 Encaminhamento e devolução | devolver de novo (sem ciclo) | 403 ou 409 | 409 | APROVADO |
| 2 Encaminhamento e devolução | origem reencaminha ao destino (mesmo protocolo) | 200 e equipe = destino | 200 equipe="Supervisão Porto Seguro" protocolo=2026-000307 | APROVADO |
| 2 Encaminhamento e devolução | encaminhar ao mesmo destino de novo | 409 ou 403 | 403 | APROVADO |
| 3 Responsável e equipe | destino assume o caso | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino muda a etapa | 200 | 200 | APROVADO |
| 3 Responsável e equipe | supervisor altera o responsável para a pessoa do destino | 200 e responsável trocado | 200 Colega Teste Supervisor de Loja | APROVADO |
| 3 Responsável e equipe | novo responsável (não supervisor) devolve a titularidade ao colega | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino altera a prioridade | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | adiciona pessoa de apoio do departamento | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio abre o caso e comenta | 200/200 | 200/200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio sem WhatsApp | restricted=true | true | APROVADO |
| 6 Apoio (incluir/remover) | apoio não encaminha nem redistribui | 403/403 | 403/403 | APROVADO |
| 6 Apoio (incluir/remover) | apoio não abre outro protocolo | 403/404 | 403 | APROVADO |
| 4 Interações | interações do destino e da origem registradas | 200/200 e visíveis para ambos | 200/200 | APROVADO |
| 5 Discussão interna compartilhada | mesmo canal para os três e mesmas mensagens, sem duplicar | mesmo canal; 3 msgs iguais | mesmoCanal=true msgs=3 | APROVADO |
| 5 Discussão interna compartilhada | outra loja e outra organização não leem a discussão | 403/404 | 404/404 | APROVADO |
| 5 Discussão interna compartilhada | a mesma discussão aparece no Chat Interno central | canal listado | true | APROVADO |
| 7 PDF e imagem (prévia inline) | anexa PDF (restrito) e PNG (aberto) com veredito limpo | 200 e scan clean | 200/200 clean/clean | APROVADO |
| 7 PDF e imagem (prévia inline) | prévia inline do PDF (tipo pelos bytes, CSP sandbox, nosniff) | 200 application/pdf inline | 200 application/pdf/inline; filename*=UTF-8''laudo.pdf/true/nosniff | APROVADO |
| 7 PDF e imagem (prévia inline) | apoio vê a imagem aberta inline e não o PDF restrito | 200 image/png / 403 | 200 image/png / 403 | APROVADO |
| 7 PDF e imagem (prévia inline) | origem que só acompanha não lê o PDF restrito | 403 | 403 | APROVADO |
| 8 Bloqueio sem veredito limpo | arquivo com assinatura EICAR (ClamAV real) não fica liberado | não clean e download bloqueado | 200 scan=error download=423 | APROVADO |
| 6 Apoio (incluir/remover) | remove a pessoa de apoio | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio removido perde o acesso | 403/404 | 403/403 | APROVADO |
| 9 Tempo real | destino recebeu atualizações em tempo real do caso | >0 eventos | 18 | APROVADO |
| 9 Tempo real | origem recebeu atualizações em tempo real | >0 eventos | 18 | APROVADO |
| 9 Tempo real | outra loja e outra organização não receberam nada | 0/0 | 0/0 | APROVADO |
| 10 Timeline | linha do tempo completa (abertura, encaminhamento, devolução, responsável, etapa, notas, apoio, arquivos) | todos os tipos | completa | APROVADO |
| 11 Relatório PDF | relatório PDF do caso (partes, histórico, anexos, emissão, datas pt-BR) | 200 + conteúdo | 200 7511 chars | APROVADO |
| 11 Relatório PDF | origem não exporta a conversa do cliente | 403 | 403 | APROVADO |
| 12 Restrições de acesso | outra loja e outra organização: caso, anexos (inclusive URL direta e inline), WhatsApp, escrita, encaminhar, reassign, exportar, participantes | tudo 403/404 | [["outra loja",[403,403,403,403,403,403,403,403,403,403]],["outra organização",[404,404,404,404,404,404,404,40 | APROVADO |
| 12 Restrições de acesso | WhatsApp de outra loja não devolve mensagens | 403/404 ou restricted | 403 undefined | APROVADO |
| 12 Restrições de acesso | quem apenas acompanha não lê o WhatsApp | restricted=true | true | APROVADO |
| 12 Restrições de acesso | solicitante que só acompanha não encaminha nem altera o responsável | 403/403 | 403/403 | APROVADO |

### Supervisor de Loja para Vendas

- **Perfis/usuários:** origem suploja@perfil.local (Teste Supervisor de Loja); destino vendas@perfil.local (Teste Vendas); colega de apoio vendas.colega@perfil.local; outsiders: logistica2@perfil.local (outra loja), estrangeiro@perfil.local (outra organização).
- **Protocolo:** 2026-000309
- **Passos:** 39 verificações, 0 reprovadas. **Situação: APROVADO** (servidor real, sessões independentes, ClamAV real; prévia em navegador por perfil receptor, ver seção 4).

| Item | Passo | Esperado | Observado | Resultado |
|---|---|---|---|---|
| 1 Abertura e recebimento | origem abre o caso para a equipe de destino | 200 | 200 | APROVADO |
| 1 Abertura e recebimento | destino recebe e vê na lista | 200 + listado | 200 true | APROVADO |
| 1 Abertura e recebimento | origem continua vendo | 200 | 200 | APROVADO |
| 2 Encaminhamento e devolução | devolver sem motivo | 400 | 400 | APROVADO |
| 2 Encaminhamento e devolução | destino devolve com motivo | 200 e equipe volta à origem | 200 equipe="" | APROVADO |
| 2 Encaminhamento e devolução | devolver de novo (sem ciclo) | 403 ou 409 | 403 | APROVADO |
| 2 Encaminhamento e devolução | origem reencaminha ao destino (mesmo protocolo) | 200 e equipe = destino | 200 equipe="Vendas Porto Seguro" protocolo=2026-000309 | APROVADO |
| 2 Encaminhamento e devolução | encaminhar ao mesmo destino de novo | 409 ou 403 | 409 | APROVADO |
| 3 Responsável e equipe | destino assume o caso | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino muda a etapa | 200 | 200 | APROVADO |
| 3 Responsável e equipe | supervisor altera o responsável para a pessoa do destino | 200 e responsável trocado | 200 Colega Teste Vendas | APROVADO |
| 3 Responsável e equipe | novo responsável (não supervisor) devolve a titularidade ao colega | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino altera a prioridade | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | adiciona pessoa de apoio do departamento | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio abre o caso e comenta | 200/200 | 200/200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio sem WhatsApp | restricted=true | true | APROVADO |
| 6 Apoio (incluir/remover) | apoio não encaminha nem redistribui | 403/403 | 403/403 | APROVADO |
| 6 Apoio (incluir/remover) | apoio não abre outro protocolo | 403/404 | 403 | APROVADO |
| 4 Interações | interações do destino e da origem registradas | 200/200 e visíveis para ambos | 200/200 | APROVADO |
| 5 Discussão interna compartilhada | mesmo canal para os três e mesmas mensagens, sem duplicar | mesmo canal; 3 msgs iguais | mesmoCanal=true msgs=3 | APROVADO |
| 5 Discussão interna compartilhada | outra loja e outra organização não leem a discussão | 403/404 | 404/404 | APROVADO |
| 5 Discussão interna compartilhada | a mesma discussão aparece no Chat Interno central | canal listado | true | APROVADO |
| 7 PDF e imagem (prévia inline) | anexa PDF (restrito) e PNG (aberto) com veredito limpo | 200 e scan clean | 200/200 clean/clean | APROVADO |
| 7 PDF e imagem (prévia inline) | prévia inline do PDF (tipo pelos bytes, CSP sandbox, nosniff) | 200 application/pdf inline | 200 application/pdf/inline; filename*=UTF-8''laudo.pdf/true/nosniff | APROVADO |
| 7 PDF e imagem (prévia inline) | apoio vê a imagem aberta inline e não o PDF restrito | 200 image/png / 403 | 200 image/png / 403 | APROVADO |
| 7 PDF e imagem (prévia inline) | origem que só acompanha não lê o PDF restrito | conforme perfil | 200 | APROVADO |
| 8 Bloqueio sem veredito limpo | arquivo com assinatura EICAR (ClamAV real) não fica liberado | não clean e download bloqueado | 200 scan=error download=423 | APROVADO |
| 6 Apoio (incluir/remover) | remove a pessoa de apoio | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio removido perde o acesso | 403/404 | 403/403 | APROVADO |
| 9 Tempo real | destino recebeu atualizações em tempo real do caso | >0 eventos | 18 | APROVADO |
| 9 Tempo real | origem recebeu atualizações em tempo real | >0 eventos | 18 | APROVADO |
| 9 Tempo real | outra loja e outra organização não receberam nada | 0/0 | 0/0 | APROVADO |
| 10 Timeline | linha do tempo completa (abertura, encaminhamento, devolução, responsável, etapa, notas, apoio, arquivos) | todos os tipos | completa | APROVADO |
| 11 Relatório PDF | relatório PDF do caso (partes, histórico, anexos, emissão, datas pt-BR) | 200 + conteúdo | 200 7174 chars | APROVADO |
| 11 Relatório PDF | origem não exporta a conversa do cliente | conforme perfil | 200 | APROVADO |
| 12 Restrições de acesso | outra loja e outra organização: caso, anexos (inclusive URL direta e inline), WhatsApp, escrita, encaminhar, reassign, exportar, participantes | tudo 403/404 | [["outra loja",[403,403,403,403,403,403,403,403,403,403]],["outra organização",[404,404,404,404,404,404,404,40 | APROVADO |
| 12 Restrições de acesso | WhatsApp de outra loja não devolve mensagens | 403/404 ou restricted | 403 undefined | APROVADO |
| 12 Restrições de acesso | quem apenas acompanha não lê o WhatsApp | conforme perfil | false | APROVADO |
| 12 Restrições de acesso | solicitante que só acompanha não encaminha nem altera o responsável | conforme perfil | 200/200 | APROVADO |

### Vendas para Supervisor de Loja

- **Perfis/usuários:** origem vendas@perfil.local (Teste Vendas); destino suploja@perfil.local (Teste Supervisor de Loja); colega de apoio suploja.colega@perfil.local; outsiders: logistica2@perfil.local (outra loja), estrangeiro@perfil.local (outra organização).
- **Protocolo:** 2026-000311
- **Passos:** 39 verificações, 0 reprovadas. **Situação: APROVADO** (servidor real, sessões independentes, ClamAV real; prévia em navegador por perfil receptor, ver seção 4).

| Item | Passo | Esperado | Observado | Resultado |
|---|---|---|---|---|
| 1 Abertura e recebimento | origem abre o caso para a equipe de destino | 200 | 200 | APROVADO |
| 1 Abertura e recebimento | destino recebe e vê na lista | 200 + listado | 200 true | APROVADO |
| 1 Abertura e recebimento | origem continua vendo | 200 | 200 | APROVADO |
| 2 Encaminhamento e devolução | devolver sem motivo | 400 | 400 | APROVADO |
| 2 Encaminhamento e devolução | destino devolve com motivo | 200 e equipe volta à origem | 200 equipe="" | APROVADO |
| 2 Encaminhamento e devolução | devolver de novo (sem ciclo) | 403 ou 409 | 409 | APROVADO |
| 2 Encaminhamento e devolução | origem reencaminha ao destino (mesmo protocolo) | 200 e equipe = destino | 200 equipe="Supervisão Porto Seguro" protocolo=2026-000311 | APROVADO |
| 2 Encaminhamento e devolução | encaminhar ao mesmo destino de novo | 409 ou 403 | 403 | APROVADO |
| 3 Responsável e equipe | destino assume o caso | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino muda a etapa | 200 | 200 | APROVADO |
| 3 Responsável e equipe | supervisor altera o responsável para a pessoa do destino | 200 e responsável trocado | 200 Colega Teste Supervisor de Loja | APROVADO |
| 3 Responsável e equipe | novo responsável (não supervisor) devolve a titularidade ao colega | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino altera a prioridade | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | adiciona pessoa de apoio do departamento | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio abre o caso e comenta | 200/200 | 200/200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio sem WhatsApp | restricted=true | true | APROVADO |
| 6 Apoio (incluir/remover) | apoio não encaminha nem redistribui | 403/403 | 403/403 | APROVADO |
| 6 Apoio (incluir/remover) | apoio não abre outro protocolo | 403/404 | 403 | APROVADO |
| 4 Interações | interações do destino e da origem registradas | 200/200 e visíveis para ambos | 200/200 | APROVADO |
| 5 Discussão interna compartilhada | mesmo canal para os três e mesmas mensagens, sem duplicar | mesmo canal; 3 msgs iguais | mesmoCanal=true msgs=3 | APROVADO |
| 5 Discussão interna compartilhada | outra loja e outra organização não leem a discussão | 403/404 | 404/404 | APROVADO |
| 5 Discussão interna compartilhada | a mesma discussão aparece no Chat Interno central | canal listado | true | APROVADO |
| 7 PDF e imagem (prévia inline) | anexa PDF (restrito) e PNG (aberto) com veredito limpo | 200 e scan clean | 200/200 clean/clean | APROVADO |
| 7 PDF e imagem (prévia inline) | prévia inline do PDF (tipo pelos bytes, CSP sandbox, nosniff) | 200 application/pdf inline | 200 application/pdf/inline; filename*=UTF-8''laudo.pdf/true/nosniff | APROVADO |
| 7 PDF e imagem (prévia inline) | apoio vê a imagem aberta inline e não o PDF restrito | 200 image/png / 403 | 200 image/png / 403 | APROVADO |
| 7 PDF e imagem (prévia inline) | origem que só acompanha não lê o PDF restrito | 403 | 403 | APROVADO |
| 8 Bloqueio sem veredito limpo | arquivo com assinatura EICAR (ClamAV real) não fica liberado | não clean e download bloqueado | 200 scan=error download=423 | APROVADO |
| 6 Apoio (incluir/remover) | remove a pessoa de apoio | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio removido perde o acesso | 403/404 | 403/403 | APROVADO |
| 9 Tempo real | destino recebeu atualizações em tempo real do caso | >0 eventos | 18 | APROVADO |
| 9 Tempo real | origem recebeu atualizações em tempo real | >0 eventos | 18 | APROVADO |
| 9 Tempo real | outra loja e outra organização não receberam nada | 0/0 | 0/0 | APROVADO |
| 10 Timeline | linha do tempo completa (abertura, encaminhamento, devolução, responsável, etapa, notas, apoio, arquivos) | todos os tipos | completa | APROVADO |
| 11 Relatório PDF | relatório PDF do caso (partes, histórico, anexos, emissão, datas pt-BR) | 200 + conteúdo | 200 7535 chars | APROVADO |
| 11 Relatório PDF | origem não exporta a conversa do cliente | 403 | 403 | APROVADO |
| 12 Restrições de acesso | outra loja e outra organização: caso, anexos (inclusive URL direta e inline), WhatsApp, escrita, encaminhar, reassign, exportar, participantes | tudo 403/404 | [["outra loja",[403,403,403,403,403,403,403,403,403,403]],["outra organização",[404,404,404,404,404,404,404,40 | APROVADO |
| 12 Restrições de acesso | WhatsApp de outra loja não devolve mensagens | 403/404 ou restricted | 403 undefined | APROVADO |
| 12 Restrições de acesso | quem apenas acompanha não lê o WhatsApp | restricted=true | true | APROVADO |
| 12 Restrições de acesso | solicitante que só acompanha não encaminha nem altera o responsável | 403/403 | 403/403 | APROVADO |

### Supervisor de SAC para SAC

- **Perfis/usuários:** origem supsac@perfil.local (Teste Supervisor de SAC); destino sac@perfil.local (Teste SAC); colega de apoio sac.colega@perfil.local; outsiders: logistica2@perfil.local (outra loja), estrangeiro@perfil.local (outra organização).
- **Protocolo:** 2026-000313
- **Passos:** 39 verificações, 0 reprovadas. **Situação: APROVADO** (servidor real, sessões independentes, ClamAV real; prévia em navegador por perfil receptor, ver seção 4).

| Item | Passo | Esperado | Observado | Resultado |
|---|---|---|---|---|
| 1 Abertura e recebimento | origem abre o caso para a equipe de destino | 200 | 200 | APROVADO |
| 1 Abertura e recebimento | destino recebe e vê na lista | 200 + listado | 200 true | APROVADO |
| 1 Abertura e recebimento | origem continua vendo | 200 | 200 | APROVADO |
| 2 Encaminhamento e devolução | devolver sem motivo | 400 | 400 | APROVADO |
| 2 Encaminhamento e devolução | destino devolve com motivo | 200 e equipe volta à origem | 200 equipe="" | APROVADO |
| 2 Encaminhamento e devolução | devolver de novo (sem ciclo) | 403 ou 409 | 403 | APROVADO |
| 2 Encaminhamento e devolução | origem reencaminha ao destino (mesmo protocolo) | 200 e equipe = destino | 200 equipe="SAC Central" protocolo=2026-000313 | APROVADO |
| 2 Encaminhamento e devolução | encaminhar ao mesmo destino de novo | 409 ou 403 | 409 | APROVADO |
| 3 Responsável e equipe | destino assume o caso | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino muda a etapa | 200 | 200 | APROVADO |
| 3 Responsável e equipe | supervisor altera o responsável para a pessoa do destino | 200 e responsável trocado | 200 Colega Teste SAC | APROVADO |
| 3 Responsável e equipe | novo responsável (não supervisor) devolve a titularidade ao colega | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino altera a prioridade | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | adiciona pessoa de apoio do departamento | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio abre o caso e comenta | 200/200 | 200/200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio sem WhatsApp | restricted=true | true | APROVADO |
| 6 Apoio (incluir/remover) | apoio não encaminha nem redistribui | 403/403 | 403/403 | APROVADO |
| 6 Apoio (incluir/remover) | apoio não abre outro protocolo | 403/404 | 403 | APROVADO |
| 4 Interações | interações do destino e da origem registradas | 200/200 e visíveis para ambos | 200/200 | APROVADO |
| 5 Discussão interna compartilhada | mesmo canal para os três e mesmas mensagens, sem duplicar | mesmo canal; 3 msgs iguais | mesmoCanal=true msgs=3 | APROVADO |
| 5 Discussão interna compartilhada | outra loja e outra organização não leem a discussão | 403/404 | 404/404 | APROVADO |
| 5 Discussão interna compartilhada | a mesma discussão aparece no Chat Interno central | canal listado | true | APROVADO |
| 7 PDF e imagem (prévia inline) | anexa PDF (restrito) e PNG (aberto) com veredito limpo | 200 e scan clean | 200/200 clean/clean | APROVADO |
| 7 PDF e imagem (prévia inline) | prévia inline do PDF (tipo pelos bytes, CSP sandbox, nosniff) | 200 application/pdf inline | 200 application/pdf/inline; filename*=UTF-8''laudo.pdf/true/nosniff | APROVADO |
| 7 PDF e imagem (prévia inline) | apoio vê a imagem aberta inline e não o PDF restrito | 200 image/png / 403 | 200 image/png / 403 | APROVADO |
| 7 PDF e imagem (prévia inline) | origem que só acompanha não lê o PDF restrito | conforme perfil | 200 | APROVADO |
| 8 Bloqueio sem veredito limpo | arquivo com assinatura EICAR (ClamAV real) não fica liberado | não clean e download bloqueado | 200 scan=error download=423 | APROVADO |
| 6 Apoio (incluir/remover) | remove a pessoa de apoio | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio removido perde o acesso | 403/404 | 403/403 | APROVADO |
| 9 Tempo real | destino recebeu atualizações em tempo real do caso | >0 eventos | 18 | APROVADO |
| 9 Tempo real | origem recebeu atualizações em tempo real | >0 eventos | 18 | APROVADO |
| 9 Tempo real | outra loja e outra organização não receberam nada | 0/0 | 0/0 | APROVADO |
| 10 Timeline | linha do tempo completa (abertura, encaminhamento, devolução, responsável, etapa, notas, apoio, arquivos) | todos os tipos | completa | APROVADO |
| 11 Relatório PDF | relatório PDF do caso (partes, histórico, anexos, emissão, datas pt-BR) | 200 + conteúdo | 200 7399 chars | APROVADO |
| 11 Relatório PDF | origem não exporta a conversa do cliente | conforme perfil | 403 | APROVADO |
| 12 Restrições de acesso | outra loja e outra organização: caso, anexos (inclusive URL direta e inline), WhatsApp, escrita, encaminhar, reassign, exportar, participantes | tudo 403/404 | [["outra loja",[403,403,403,403,403,403,403,403,403,403]],["outra organização",[404,404,404,404,404,404,404,40 | APROVADO |
| 12 Restrições de acesso | WhatsApp de outra loja não devolve mensagens | 403/404 ou restricted | 403 undefined | APROVADO |
| 12 Restrições de acesso | quem apenas acompanha não lê o WhatsApp | conforme perfil | true | APROVADO |
| 12 Restrições de acesso | solicitante que só acompanha não encaminha nem altera o responsável | conforme perfil | 200/200 | APROVADO |

### SAC para Supervisor de SAC

- **Perfis/usuários:** origem sac@perfil.local (Teste SAC); destino supsac@perfil.local (Teste Supervisor de SAC); colega de apoio sac.colega@perfil.local; outsiders: logistica2@perfil.local (outra loja), estrangeiro@perfil.local (outra organização).
- **Protocolo:** 2026-000315
- **Passos:** 39 verificações, 0 reprovadas. **Situação: APROVADO** (servidor real, sessões independentes, ClamAV real; prévia em navegador por perfil receptor, ver seção 4).

| Item | Passo | Esperado | Observado | Resultado |
|---|---|---|---|---|
| 1 Abertura e recebimento | origem abre o caso para a equipe de destino | 200 | 200 | APROVADO |
| 1 Abertura e recebimento | destino recebe e vê na lista | 200 + listado | 200 true | APROVADO |
| 1 Abertura e recebimento | origem continua vendo | 200 | 200 | APROVADO |
| 2 Encaminhamento e devolução | devolver sem motivo | 400 | 400 | APROVADO |
| 2 Encaminhamento e devolução | destino devolve com motivo | 200 e equipe volta à origem | 200 equipe="" | APROVADO |
| 2 Encaminhamento e devolução | devolver de novo (sem ciclo) | 403 ou 409 | 409 | APROVADO |
| 2 Encaminhamento e devolução | origem reencaminha ao destino (mesmo protocolo) | 200 e equipe = destino | 200 equipe="Supervisão SAC" protocolo=2026-000315 | APROVADO |
| 2 Encaminhamento e devolução | encaminhar ao mesmo destino de novo | 409 ou 403 | 403 | APROVADO |
| 3 Responsável e equipe | destino assume o caso | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino muda a etapa | 200 | 200 | APROVADO |
| 3 Responsável e equipe | supervisor altera o responsável para a pessoa do destino | 200 e responsável trocado | 200 Colega Teste SAC | APROVADO |
| 3 Responsável e equipe | novo responsável (não supervisor) devolve a titularidade ao colega | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino altera a prioridade | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | adiciona pessoa de apoio do departamento | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio abre o caso e comenta | 200/200 | 200/200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio sem WhatsApp | restricted=true | true | APROVADO |
| 6 Apoio (incluir/remover) | apoio não encaminha nem redistribui | 403/403 | 403/403 | APROVADO |
| 6 Apoio (incluir/remover) | apoio não abre outro protocolo | 200 (view_all do perfil) | 403 | APROVADO |
| 4 Interações | interações do destino e da origem registradas | 200/200 e visíveis para ambos | 200/200 | APROVADO |
| 5 Discussão interna compartilhada | mesmo canal para os três e mesmas mensagens, sem duplicar | mesmo canal; 3 msgs iguais | mesmoCanal=true msgs=3 | APROVADO |
| 5 Discussão interna compartilhada | outra loja e outra organização não leem a discussão | 403/404 | 404/404 | APROVADO |
| 5 Discussão interna compartilhada | a mesma discussão aparece no Chat Interno central | canal listado | true | APROVADO |
| 7 PDF e imagem (prévia inline) | anexa PDF (restrito) e PNG (aberto) com veredito limpo | 200 e scan clean | 200/200 clean/clean | APROVADO |
| 7 PDF e imagem (prévia inline) | prévia inline do PDF (tipo pelos bytes, CSP sandbox, nosniff) | 200 application/pdf inline | 200 application/pdf/inline; filename*=UTF-8''laudo.pdf/true/nosniff | APROVADO |
| 7 PDF e imagem (prévia inline) | apoio vê a imagem aberta inline e não o PDF restrito | 200 image/png / 403 | 200 image/png / 403 | APROVADO |
| 7 PDF e imagem (prévia inline) | origem que só acompanha não lê o PDF restrito | 403 | 403 | APROVADO |
| 8 Bloqueio sem veredito limpo | arquivo com assinatura EICAR (ClamAV real) não fica liberado | não clean e download bloqueado | 200 scan=error download=423 | APROVADO |
| 6 Apoio (incluir/remover) | remove a pessoa de apoio | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio removido perde o acesso | 200 (view_all) | 403/403 | APROVADO |
| 9 Tempo real | destino recebeu atualizações em tempo real do caso | >0 eventos | 18 | APROVADO |
| 9 Tempo real | origem recebeu atualizações em tempo real | >0 eventos | 18 | APROVADO |
| 9 Tempo real | outra loja e outra organização não receberam nada | 0/0 | 0/0 | APROVADO |
| 10 Timeline | linha do tempo completa (abertura, encaminhamento, devolução, responsável, etapa, notas, apoio, arquivos) | todos os tipos | completa | APROVADO |
| 11 Relatório PDF | relatório PDF do caso (partes, histórico, anexos, emissão, datas pt-BR) | 200 + conteúdo | 200 7401 chars | APROVADO |
| 11 Relatório PDF | origem não exporta a conversa do cliente | 403 | 403 | APROVADO |
| 12 Restrições de acesso | outra loja e outra organização: caso, anexos (inclusive URL direta e inline), WhatsApp, escrita, encaminhar, reassign, exportar, participantes | tudo 403/404 | [["outra loja",[403,403,403,403,403,403,403,403,403,403]],["outra organização",[404,404,404,404,404,404,404,40 | APROVADO |
| 12 Restrições de acesso | WhatsApp de outra loja não devolve mensagens | 403/404 ou restricted | 403 undefined | APROVADO |
| 12 Restrições de acesso | quem apenas acompanha não lê o WhatsApp | restricted=true | true | APROVADO |
| 12 Restrições de acesso | solicitante que só acompanha não encaminha nem altera o responsável | 403/403 | 403/403 | APROVADO |

### SAC para Logística (extra)

- **Perfis/usuários:** origem sac@perfil.local (Teste SAC); destino logistica@perfil.local (Teste Logística); colega de apoio logistica.colega@perfil.local; outsiders: logistica2@perfil.local (outra loja), estrangeiro@perfil.local (outra organização).
- **Protocolo:** 2026-000317
- **Passos:** 39 verificações, 0 reprovadas. **Situação: APROVADO** (servidor real, sessões independentes, ClamAV real; prévia em navegador por perfil receptor, ver seção 4).

| Item | Passo | Esperado | Observado | Resultado |
|---|---|---|---|---|
| 1 Abertura e recebimento | origem abre o caso para a equipe de destino | 200 | 200 | APROVADO |
| 1 Abertura e recebimento | destino recebe e vê na lista | 200 + listado | 200 true | APROVADO |
| 1 Abertura e recebimento | origem continua vendo | 200 | 200 | APROVADO |
| 2 Encaminhamento e devolução | devolver sem motivo | 400 | 400 | APROVADO |
| 2 Encaminhamento e devolução | destino devolve com motivo | 200 e equipe volta à origem | 200 equipe="" | APROVADO |
| 2 Encaminhamento e devolução | devolver de novo (sem ciclo) | 403 ou 409 | 403 | APROVADO |
| 2 Encaminhamento e devolução | origem reencaminha ao destino (mesmo protocolo) | 200 e equipe = destino | 200 equipe="Logistica Porto Seguro" protocolo=2026-000317 | APROVADO |
| 2 Encaminhamento e devolução | encaminhar ao mesmo destino de novo | 409 ou 403 | 403 | APROVADO |
| 3 Responsável e equipe | destino assume o caso | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino muda a etapa | 200 | 200 | APROVADO |
| 3 Responsável e equipe | supervisor altera o responsável para a pessoa do destino | 200 e responsável trocado | 200 Colega Teste Log�stica | APROVADO |
| 3 Responsável e equipe | novo responsável (não supervisor) devolve a titularidade ao colega | 200 | 200 | APROVADO |
| 3 Responsável e equipe | destino altera a prioridade | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | adiciona pessoa de apoio do departamento | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio abre o caso e comenta | 200/200 | 200/200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio sem WhatsApp | restricted=true | true | APROVADO |
| 6 Apoio (incluir/remover) | apoio não encaminha nem redistribui | 403/403 | 403/403 | APROVADO |
| 6 Apoio (incluir/remover) | apoio não abre outro protocolo | 403/404 | 403 | APROVADO |
| 4 Interações | interações do destino e da origem registradas | 200/200 e visíveis para ambos | 200/200 | APROVADO |
| 5 Discussão interna compartilhada | mesmo canal para os três e mesmas mensagens, sem duplicar | mesmo canal; 3 msgs iguais | mesmoCanal=true msgs=3 | APROVADO |
| 5 Discussão interna compartilhada | outra loja e outra organização não leem a discussão | 403/404 | 404/404 | APROVADO |
| 5 Discussão interna compartilhada | a mesma discussão aparece no Chat Interno central | canal listado | true | APROVADO |
| 7 PDF e imagem (prévia inline) | anexa PDF (restrito) e PNG (aberto) com veredito limpo | 200 e scan clean | 200/200 clean/clean | APROVADO |
| 7 PDF e imagem (prévia inline) | prévia inline do PDF (tipo pelos bytes, CSP sandbox, nosniff) | 200 application/pdf inline | 200 application/pdf/inline; filename*=UTF-8''laudo.pdf/true/nosniff | APROVADO |
| 7 PDF e imagem (prévia inline) | apoio vê a imagem aberta inline e não o PDF restrito | 200 image/png / 403 | 200 image/png / 403 | APROVADO |
| 7 PDF e imagem (prévia inline) | origem que só acompanha não lê o PDF restrito | 403 | 403 | APROVADO |
| 8 Bloqueio sem veredito limpo | arquivo com assinatura EICAR (ClamAV real) não fica liberado | não clean e download bloqueado | 200 scan=error download=423 | APROVADO |
| 6 Apoio (incluir/remover) | remove a pessoa de apoio | 200 | 200 | APROVADO |
| 6 Apoio (incluir/remover) | apoio removido perde o acesso | 403/404 | 403/403 | APROVADO |
| 9 Tempo real | destino recebeu atualizações em tempo real do caso | >0 eventos | 18 | APROVADO |
| 9 Tempo real | origem recebeu atualizações em tempo real | >0 eventos | 18 | APROVADO |
| 9 Tempo real | outra loja e outra organização não receberam nada | 0/0 | 0/0 | APROVADO |
| 10 Timeline | linha do tempo completa (abertura, encaminhamento, devolução, responsável, etapa, notas, apoio, arquivos) | todos os tipos | completa | APROVADO |
| 11 Relatório PDF | relatório PDF do caso (partes, histórico, anexos, emissão, datas pt-BR) | 200 + conteúdo | 200 7521 chars | APROVADO |
| 11 Relatório PDF | origem não exporta a conversa do cliente | 403 | 403 | APROVADO |
| 12 Restrições de acesso | outra loja e outra organização: caso, anexos (inclusive URL direta e inline), WhatsApp, escrita, encaminhar, reassign, exportar, participantes | tudo 403/404 | [["outra loja",[403,403,403,403,403,403,403,403,403,403]],["outra organização",[404,404,404,404,404,404,404,40 | APROVADO |
| 12 Restrições de acesso | WhatsApp de outra loja não devolve mensagens | 403/404 ou restricted | 403 undefined | APROVADO |
| 12 Restrições de acesso | quem apenas acompanha não lê o WhatsApp | restricted=true | true | APROVADO |
| 12 Restrições de acesso | solicitante que só acompanha não encaminha nem altera o responsável | 403/403 | 403/403 | APROVADO |


## 4. Evidência de navegador

| Evidência | Resultado |
|---|---|
| Prévia no navegador (iframe de PDF e imagem) com Vendas, SAC, Logística, Supervisor de Loja e Supervisor de SAC, cada um em um caso que recebeu | 5 de 5 renderizaram PDF e imagem; o arquivo sem veredito limpo não oferece botão de prévia |
| Encaminhar (Logística → Vendas) e Devolver (Vendas → Logística, com o destino anterior mostrado e motivo obrigatório) na tela | APROVADO; timeline atualizada |
| Teste simultâneo ao vivo: tela de um perfil aberta, duas sessões independentes enviando 10 mensagens ao mesmo tempo, mudando etapa e criando nota | 10 mensagens únicas, etapa e timeline atualizadas sem recarregar |
| Defeito achado ao vivo: tela perdia "Equipe"/"Departamento atual" após atualização em tempo real | corrigido; confirmado na tela e por teste de regressão |
| Perda de autorização: outra sessão encaminha o caso para fora da equipe | tela mostra "Ocorrência não encontrada"; caso, anexos e URL direta de anexo → 403; caso some da lista |
| Versão celular do card de encaminhamento | APROVADO |

## 5. Teste simultâneo (duas ou mais sessões independentes)

| Id | Verificação | Esperado | Observado | Resultado |
|---|---|---|---|---|
| A1 | mensagens simultâneas de duas pessoas: sem duplicar nem perder | 40 mensagens únicas | 40 únicas=40 | APROVADO |
| A2 | a discussão é a mesma para os dois (mesma ordem e ids) | idêntica | true | APROVADO |
| A3 | nenhuma mensagem fora do caso chega a quem não tem acesso (outra loja) | 403/404 | 404 | APROVADO |
| A4 | tempo real: cada um recebeu as mensagens do outro | ≥20 cada | logistica=27 suploja=27 | APROVADO |
| A5 | tempo real: quem não tem acesso não recebeu nada do canal | 0 | 0 | APROVADO |
| B1 | encaminhamento concorrente: um vence, o outro recebe conflito | 200/409 (ou 403 se já perdeu o acesso) | 200/409 | APROVADO |
| B2 | um único registro de movimentação e a equipe final é a do vencedor | 1 registro | 1 equipe=Supervisão Porto Seguro | APROVADO |
| B3 | timeline atualizada em tempo real para quem tem acesso | evento recebido | 1 | APROVADO |
| C1 | alteração de etapa simultânea: sem 5xx e etapa final é uma das duas | 200/200 ou 200/409 | 200/409 etapa=Em análise | APROVADO |
| D1 | o mesmo apoio incluído por duas pessoas ao mesmo tempo: um só vínculo | 1 vínculo ativo | 200/200 vínculos=1 | APROVADO |
| E1 | dois usuários visualizam o mesmo anexo ao mesmo tempo | 200/200 e bytes iguais | 200/200 | APROVADO |
| F0 | antes: Logística (equipe) vê o caso e lê o anexo restrito de outra pessoa | 200/200 | 200/200 | APROVADO |
| F1 | Logística encaminha para Vendas com responsável | 200 | 200 | APROVADO |
| F2 | COLETIVO: colega da equipe anterior, não envolvido, perde o acesso ao caso | 403/404 | 403 | APROVADO |
| F3 | INDIVIDUAL: quem encaminhou continua vendo e comentando | 200/200 | 200/200 | APROVADO |
| F4 | INDIVIDUAL: quem encaminhou não muda etapa, não encaminha de novo, não lê anexo restrito de outra pessoa, sem WhatsApp | 403/403/403/restricted | 403/403/403/true | APROVADO |
| F5 | o novo responsável (Vendas) acessa o caso e os anexos que vieram com ele | 200/200 | 200/200 | APROVADO |
| F6 | URL direta do anexo e prévia para quem perdeu a autorização | 403/404 | 403 | APROVADO |

Acesso individual × coletivo (itens F2–F6): colega da equipe anterior não envolvido perde o caso (coletivo); quem encaminhou mantém só o que um acompanhante pode (ver e comentar), sem etapa, sem reencaminhar, sem anexo restrito alheio, sem WhatsApp (individual).

## 6. Três departamentos num caso

SAC pede, Logística trabalha, Vendas entra como apoio escolhido pelo supervisor, supervisão da loja acompanha. Todos veem a discussão completa; Vendas como apoio não lê WhatsApp nem arquivo restrito; o PDF lista os três departamentos e as pessoas. **APROVADO no teste Go (`TestHomologation_ThreeDepartmentsOnOneCase`)**; não foi repetido no navegador nesta rodada (o ciclo de apoio/PDF por perfil está nos 12 cenários acima).

## 7. Testes negativos

| Id | Verificação | Esperado | Observado | Resultado |
|---|---|---|---|---|
| N1 | sem chat:read/chat:write mas na equipe: ainda trabalha o caso | 200 | 200 | APROVADO |
| N2 | sem chat:read: conversa do cliente restrita | restricted=true | true | APROVADO |
| N3 | sem chat:write: não envia ao cliente | 403 | 403 | APROVADO |
| N4 | sem chat:read: não exporta a conversa | 403 | 403 | APROVADO |
| N5 | quem trabalha o caso lê o anexo restrito (regra do produto) e a prévia sai inline | 200 | 200 | APROVADO |
| N6-outra organização | caso, anexos, URL direta, inline, alterar responsável, encaminhar, auto-participar, editar, excluir | tudo 403/404 | 404/404/404/404/404/404/404/404/403 | APROVADO |
| N6-outra loja | caso, anexos, URL direta, inline, alterar responsável, encaminhar, auto-participar, editar, excluir | tudo 403/404 | 403/403/403/403/403/403/403/403/403 | APROVADO |
| N6-Vendas sem participação | caso, anexos, URL direta, inline, alterar responsável, encaminhar, auto-participar, editar, excluir | tudo 403/404 | 403/403/403/403/403/403/403/403/403 | APROVADO |
| N7 | acompanhando tenta alterar responsável | 403 | 403 | APROVADO |
| N8 | acompanhando sem WhatsApp | restricted=true | true | APROVADO |
| N9 | acompanhando: prévia/download do anexo restrito | 403 | 403 | APROVADO |
| N10 | acompanhando tenta mudar a etapa | 403 | 403 | APROVADO |
| N11 | arquivo com assinatura EICAR enviado por quem acompanha não chega a ficar visível | não clean e download bloqueado | 200 error dl=423 | APROVADO |

Também aprovados em Go (perfis reais): outra organização/outra loja/sem vínculo/apoio sem vínculo (`TestHomologation_ForbiddenAttempts`), apoio de departamento não autorizado, participar da discussão não dá WhatsApp nem anexo restrito, WhatsApp simulado sem mensagem real (`TestHomologation_WhatsAppSimulation`).

## 8. Falhas encontradas e correções

1. **Congelamento do servidor com mensagens simultâneas** (esgotamento do pool de 25 conexões: cada requisição segurava uma transação e pedia outra conexão; 40 mensagens no mesmo protocolo travaram o backend). Defeito pré-existente no envio do Chat Interno, replicado por mim no encaminhamento. Correção: o acesso à discussão do caso é decidido **antes** da transação (6 pontos) e a decisão de "quem continua acompanhando" saiu da transação de encaminhamento. Regressão: `TestHomologation_ConcurrentWritesDoNotExhaustTheConnectionPool` (pool de 4 conexões; falha sem a correção, passa com ela). Commit `94488e9d`.
2. **Atualização em tempo real sem nome da equipe**: a tela perdia "Equipe"/"Departamento atual". Correção no `broadcastOccurrence`; regressão `TestHomologation_PushedUpdateCarriesTheTeamName`. Commit `c4a0b0eb`.
3. Corrida na reordenação de etapas (primeira rodada Go) — corrigida na rodada anterior (bloqueio da linha da organização).
4. Apoio sem permissões explícitas ficava sem poder comentar — padrão agora é comentar.
5. Suposições de teste corrigidas (não eram defeitos): WhatsApp restrito responde 200 `restricted:true` sem mensagens (tratado como negado); Supervisor de SAC vê tudo por `view_all`; caso aberto por quem já está na equipe destino fica com quem abriu e o destino o recebe por redistribuição; novo responsável lê os arquivos do caso.

## 9. Permissões do administrador

Causa: a sincronização só liga permissões a papéis de sistema que não têm nenhuma; as 4 ausentes (`occurrences:delete`, `sales_opportunities:delete`, `sales_opportunities:view_team`, `analytics.agents:view_team`) foram criadas depois do papel (que nunca foi editado). Nada foi concedido. Proposta aditiva, com preservação de remoções intencionais, tratamento especial das permissões destrutivas, dry-run, snapshot e auditoria: `02-proposta-migracao-permissoes.md`. **Não executada.**

## 10. Regressões

- Go: ver seção 11. Frontend: `vue-tsc` limpo; vitest 58 arquivos, 458 testes.
- Falhas conhecidas e anteriores (não são regressões): `TestServeLoginBackground_RejectsSymlink`, `TestApp_ServeMedia_RejectsSymlink` (symlink no Windows) e `TestUpdateContactChatbotMessage_SetsTimestampAndResetsReminder`.


## 11. Resultado final da suíte Go e pendências

**Suíte Go:** 24 pacotes `ok`; o pacote `internal/handlers` (rodado sozinho com `-timeout 40m`, 1250 s) tem **4 falhas**:

| Teste | Natureza |
|---|---|
| `TestServeLoginBackground_RejectsSymlink` | conhecida, anterior (symlink no Windows) |
| `TestApp_ServeMedia_RejectsSymlink` | conhecida, anterior (symlink no Windows) |
| `TestUpdateContactChatbotMessage_SetsTimestampAndResetsReminder` | conhecida, anterior |
| `TestPhaseA_Concurrency_NoDeadlockUnderMixedWrites` | **limiar de desempenho** (`total > 100` operações em 2 s num canal livre: observado 32–87). **Não é regressão das correções desta rodada:** reproduz de forma idêntica no commit anterior às correções (`62991ddf`) e o teste só exerce canais livres (a mudança do pool não altera esse caminho). Passou na rodada completa anterior no mesmo ambiente; sensível à carga. Precisa de investigação separada. |

O pacote estourou o timeout padrão de 10 min na rodada completa (os testes novos somam tempo); por isso foi rodado isolado com timeout maior.

**Pendências por gravidade**

- **Alta (impede aprovação de merge):** (1) decidir/investigar `TestPhaseA_Concurrency_NoDeadlockUnderMixedWrites` (ou confirmar flaky no CI); (2) conferir os papéis de sistema de produção e aprovar/rejeitar a migração aditiva de permissões (`02-...md`) **antes de qualquer deploy**; (3) homologação em **dois navegadores simultâneos reais** (só um navegador estava disponível) e, se exigida, a tela de cada uma das 12 combinações.
- **Média:** veredito *infected* do ClamAV real não demonstrado neste host (Windows Defender remove o EICAR) — repetir em ambiente sem antivírus no host; cenário de três departamentos só em Go (não no navegador); casos encaminhados antes do registro de movimentações usam o evento "encaminhado" como origem.
- **Baixa:** gráficos de analytics de outras telas além dos corrigidos; o relatório PDF não afirma autenticidade (código de referência só ligado ao log de auditoria); dados de teste criados no banco de desenvolvimento (equipes, usuários colegas, organização estrangeira).

## 12. Recomendação sobre prontidão para merge

**Não aprovar o merge ainda; a branch está tecnicamente muito perto.** A favor: funcionalidade completa (L1, apoio, PDF, ordenação, datas, prévia), 12 cenários cruzados × 39 verificações aprovados no servidor real com perfis reais, simultaneidade e negativos aprovados, dois defeitos reais (congelamento do pool de conexões e perda do nome da equipe em tempo real) encontrados **justamente pela homologação operacional** e corrigidos com regressão, nenhuma permissão concedida para passar. Contra / pendente: um teste de desempenho do Chat Interno falhando de forma reproduzível no ambiente atual (precisa ser explicado), a decisão sobre a migração de permissões dos papéis de sistema (bloqueia deploy, não o merge em si), e a homologação em dois navegadores reais simultâneos. Critério de encerramento sugerido: explicar ou estabilizar o teste de desempenho, executar o teste dos dois navegadores, e só então reavaliar.
