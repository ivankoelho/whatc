# Integração de origin/development na feature/team-role-profiles

Data: 27/10/2026. Escopo: integrar `origin/development` **na feature**. Não houve push, merge da feature em `development`, alteração de `main` ou `feature/otp-security`, deploy, migração em produção, LDAP, expurgo ou WhatsApp real.

## 1. Referências

| | SHA |
|---|---|
| Feature antes da integração | `63e10b547d377fb670c85bb55cc00f46a3978c24` (referência de recuperação: `recover/pre-dev-integration-2026-10-27`) |
| `origin/development` integrado | `d5bfeb319ac8ee456f570e3df304bfeda1461fd2` (merge do PR #138, proteção OTP; 12 commits) |
| Base comum | `8ea4394891e353c22f026e2bf01ef6972ee0b067` |

O `development` não alterou permissões nem migrações: só trouxe os subcomandos de CLI `verify-template-names`, `otp-uncertain`, `otp-validity-report`, `audit-queue-otp`, `mask-auth-codes` e o código OTP (worker, mensagens, cliente WhatsApp, configuração `[otp]`).

## 2. Arquivos em conflito e decisões

| Arquivo | Decisão |
|---|---|
| `cmd/whatomate/main.go` | mantidos **todos** os subcomandos dos dois lados (`antimalware` e os cinco do OTP) |
| `config.example.toml` | mantidas as seções `[antimalware]` e `[otp]`, sem duplicação nem segredo |
| `internal/handlers/messages.go` | **os dois comportamentos coexistem, na ordem segura**: o mascaramento de autenticação do `development` é preservado; metadados extras (usados pelas notificações de caso) só entram em mensagens **que não são de autenticação**, nunca sobrescrevem chave já definida e nunca podem plantar `auth_code_masked`. Copiar metadados do chamador depois da sanitização era o caminho para o código reentrar na mensagem |
| `internal/worker/worker.go` | portão antimalware de mídia **antes** do caminho OTP (`sendSecretRecipient`); ver seção 3 |
| `pkg/whatsapp/types.go`, `client.go` | adotado o contrato do `development`: `APIError{Status, Code}`, classificação `Classify` (recusado, transitório, desconhecido) e redação de segredos; removida a duplicata `APIError`/`TransportError` da feature. Consumidor único afetado: o notificador de casos, reescrito sobre a classificação do provedor |
| `internal/handlers/homologation_test.go` | só renomeado o auxiliar `countStatus` → `countScanStatus` (colidia com um do `development`) |

## 3. Política de mídia de campanha (portão antimalware)

`campaignMediaGate`, antes de qualquer envio, inclusive o de código OTP:

- **Cópia local** (`header_media_local_path`, controlada pelo WhatC): só veredito **limpo** libera. **Ausência de linha de varredura não é limpo**: uma linha `pending` é registrada na hora (assim o worker de varredura alcança o arquivo e o prazo tem início persistido). Pendente/erro: o job é retido e volta à fila. Depois de **6 h** contadas de `file_scans.created_at` (persistido: reiniciar o worker ou repetir o job **não** reinicia o prazo), o destinatário **falha** com "não pôde ser verificada". O reprocessamento da fila só acontece após 5 min de ociosidade: no máximo ~72 tentativas baratas por destinatário nesse prazo. Infectada: falha imediata.
- **ID hospedado na Meta sem cópia local:** se o servidor enviou (`meta_media_uploads`, registrado só após varredura limpa), a procedência está provada. Caso contrário é **mídia histórica não inspecionável**: campanhas normais seguem enviando (aviso em log) e **mensagens com código OTP nunca enviam** essa mídia (o destinatário falha, o código segue selado). **Risco residual documentado:** um ID hospedado na Meta, por si só, não prova passagem pelo ClamAV; recomenda-se um contador de envios por esse caminho (hoje só há aviso em log por envio; sem dados sensíveis).
- Mudança em um teste preexistente: `TestWorker_HandleRecipientJob_Success` tinha cópia local sem linha de varredura e enviava; o fixture agora registra o veredito limpo, como o upload real já faz.

## 4. Erros do provedor

- Meta decide o que o erro **prova** (código de erro + 4xx): recusa documentada → falha definitiva; limite documentado → retentativa; **4xx com código não documentado, 5xx, timeout, resposta ilegível → desconhecido**: nunca reenviado sozinho.
- **OTP:** desconhecido → `delivery_uncertain` (reivindicação mantida, uma única requisição, redelivery não reenvia); identificável pelo prefixo e pelo CLI `otp-uncertain`; a liberação manual exige `allow_local_release`, **desligado por padrão**. **Notificações de caso:** `uncertain` só volta com supervisor e `confirm_duplicate_risk` explícito, com auditoria.
- **Nada de corpo bruto de resposta** em texto armazenado ou exibido: novo `whatsapp.PublicErrorText` (corta o corpo, mantém "API returned status N"), usado no worker (caminho normal e OTP) e no notificador. O código OTP continua redigido mesmo quando a Meta o repete na resposta.

## 5. Testes novos de regressão

`worker_media_otp_test.go`, `worker_gate_review_test.go` (portão de mídia: infectada/pendente/erro/limpa, sem linha, prazo persistido, procedência na Meta, campanha normal intacta; 4xx desconhecido sem reenvio e sem corpo bruto, nos dois caminhos), `integration_otp_metadata_test.go` (metadados não reintroduzem o código nem forjam a marca; classificação de erros), `public_error_test.go`.

## 6. Resultados

- Compilação e `go vet` (com testes) limpos; sem marcadores de conflito; sem mudança inesperada de permissões ou de migrações.
- **Suíte Go completa** (banco e Redis isolados, `-p 1`, nada competindo, 1125 s): **27 pacotes `ok`; `internal/handlers` com 3 falhas, todas preexistentes**; 2966 testes aprovados, 12 ignorados, **nenhuma falha nova**:
  `TestServeLoginBackground_RejectsSymlink` e `TestApp_ServeMedia_RejectsSymlink` (só no Windows; passam em Linux), `TestUpdateContactChatbotMessage_SetsTimestampAndResetsReminder` (instabilidade de precisão da asserção).
- Depois dessa rodada houve **uma única alteração de código**: o marco persistido do prazo (linha pendente quando não há linha de varredura), que só toca `internal/worker/worker.go`. Revalidado: pacote `worker` e `pkg/whatsapp` completos, os testes de `handlers` que importam o worker (OTP, campanhas, vazamento, ciclo de retentativa, notificador, mensagens), `cmd/whatomate`, compilação e `vet`.
- **Frontend:** `vue-tsc` limpo; vitest 60 arquivos, 478 testes aprovados.
- Falhas preexistentes **não foram modificadas** nem tratadas como inofensivas (análise em `03-revisao-pre-merge.md`).

## 6-B. Avaliação final: varredura de erros do provedor (após o merge)

Auditados todos os consumidores de `ParseMetaAPIError` e os pontos que gravam ou registram erro do WhatsApp (chatbot, templates, campanhas, notificações de caso, OTP, webhooks, chamadas). Exposição real encontrada e corrigida:

- O erro genérico (resposta que **não** é um objeto de erro da Meta: página HTML de proxy, eco da requisição) trazia o **corpo bruto** no texto. Agora é só `API returned status N`; status e código seguem intactos, então a classificação (recusado / transitório / desconhecido) e o `delivery_uncertain` não mudam.
- Upload de mídia, upload retomável, troca de token e `call_id` ilegível ecoavam o corpo: passam a dizer só o status e, quando houver, a mensagem estruturada da Meta.
- `messages.error_message` (e o broadcast) e o `error_message` do log de chamadas gravavam o erro cru: passam por `PublicErrorText`.
- **Mensagens estruturadas da Meta:** o código OTP já era mascarado (teste existente). Faltavam o **token de acesso** (agora mascarado em toda chamada) e o **código de autorização e segredo do app** na troca de token (mascarados). Telefones são dado pessoal, não segredo: continuam legíveis de propósito (o agente já vê o contato).
- Testes: `error_body_leak_test.go` (corpo bruto nunca entra; erro da Meta mantém código e mensagem; token, código e segredo mascarados em mensagem estruturada; classificação preservada) e `message_error_leak_test.go` (o que é gravado em `messages.error_message`).

**Limites da auditoria (não testados, documentados):**
- A redação é por **valor conhecido** (token da conta, código, segredo do app, parâmetros OTP da requisição), aplicada na origem (`doRequest` e nos erros de upload/troca de token), antes de o erro chegar a log, banco, WebSocket ou resposta HTTP. Um segredo que a Meta devolvesse **transformado** (codificado, truncado, em URL ou em campo fora de `message`/`error_user_msg`/`details`) não seria reconhecido; o corpo bruto, porém, nunca entra em erro.
- **Privacidade de telefones:** legíveis no atendimento autorizado, mas logs técnicos e mensagens exibidas a outros perfis têm públicos diferentes. Risco registrado; revisar a necessidade de exposição por destino fica fora do escopo desta integração.

Cobertura das 6 h de carência da mídia: coberta pelos testes do portão (marco persistido em `file_scans.created_at`, retentativa e reinício não reiniciam o prazo, pendente/erro mantêm o job, expirado falha o destinatário, infectada falha na hora).

### Resultado da suíte final (árvore endurecida)

Suíte Go completa, banco e Redis isolados, `-p 1`, nada competindo, 1022 s, sem alteração de código durante a execução: **27 pacotes `ok`; `internal/handlers` com 2 falhas, ambas preexistentes** (`TestServeLoginBackground_RejectsSymlink` e `TestApp_ServeMedia_RejectsSymlink`, só no Windows); 4319 testes aprovados (inclui subtestes), 14 ignorados, **nenhuma falha nova**. A instabilidade `TestUpdateContactChatbotMessage_SetsTimestampAndResetsReminder` não ocorreu nesta rodada (falhou na anterior). Testes direcionados posteriores ao endurecimento: `pkg/whatsapp`, `worker`, `calling`, `cmd/whatomate` e o filtro de envio/OTP/campanhas/templates/vazamento/mídia em `handlers` aprovados (exceto o symlink conhecido). Frontend inalterado desde a última verificação (`vue-tsc` limpo; vitest 60 arquivos, 478 testes).

## 7. Riscos de segurança residuais

1. Mídia histórica na Meta sem procedência é enviada em campanhas normais (compatibilidade); contador dedicado é pendência.
2. (Resolvido na seção 6-B.) Telefones em mensagens estruturadas da Meta continuam legíveis: dado pessoal, não segredo.
3. Retentativas de mídia retida geram até ~72 reprocessamentos por destinatário em 6 h; aceitável, mas observar em campanhas grandes.
4. O restante já registrado em `03-revisao-pre-merge.md` (auditoria da reordenação de etapas, visibilidade de membros entre lojas, migração de permissões do admin, ClamAV de produção).

## 8. Parecer atualizado

**APTO PARA AVALIAÇÃO DE MERGE da feature em `development`, em ambiente de desenvolvimento, com os riscos residuais acima.** Os conflitos foram resolvidos preservando a proteção OTP, o antimalware e o contrato de erros, com regressões novas, sem falha nova na suíte completa. **Não autoriza deploy:** continuam bloqueantes para produção a migração aditiva de permissões do administrador, a homologação do ClamAV e da infraestrutura de produção, o backup e o plano de implantação e reversão.
