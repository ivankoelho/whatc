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

## 7. Riscos de segurança residuais

1. Mídia histórica na Meta sem procedência é enviada em campanhas normais (compatibilidade); contador dedicado é pendência.
2. A resposta bruta de erros no `ParseMetaAPIError` ainda contém o corpo para consumidores internos que registram erros em log (fluxos, templates); o que é **armazenado/exibido** nos caminhos de campanha e de notificação passa por `PublicErrorText`. Vale uma varredura dos demais consumidores.
3. Retentativas de mídia retida geram até ~72 reprocessamentos por destinatário em 6 h; aceitável, mas observar em campanhas grandes.
4. O restante já registrado em `03-revisao-pre-merge.md` (auditoria da reordenação de etapas, visibilidade de membros entre lojas, migração de permissões do admin, ClamAV de produção).

## 8. Parecer atualizado

**APTO PARA AVALIAÇÃO DE MERGE da feature em `development`, em ambiente de desenvolvimento, com os riscos residuais acima.** Os conflitos foram resolvidos preservando a proteção OTP, o antimalware e o contrato de erros, com regressões novas, sem falha nova na suíte completa. **Não autoriza deploy:** continuam bloqueantes para produção a migração aditiva de permissões do administrador, a homologação do ClamAV e da infraestrutura de produção, o backup e o plano de implantação e reversão.
