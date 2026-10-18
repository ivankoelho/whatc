# Cliente de e-mail no Whatc: especificação

Status: proposta, sem código. Data: 2026-10-15.

## 1. Objetivo

Trazer o e-mail para o mesmo lugar do atendimento: o agente lê, responde e vê o
histórico de e-mail de um contato ao lado das conversas de WhatsApp, usando as
caixas que a empresa já tem (Gmail, Microsoft 365, IMAP genérico).

Não é objetivo: hospedar domínio ou servidor de e-mail, calendário, contatos
CardDAV, criptografia OpenPGP/S/MIME, regras Sieve. O `penguin-mail` (GPL-3.0,
aplicativo desktop em Rust/GTK) foi avaliado e **não é reaproveitável**: serve só
de referência de funcionalidades. Nenhum código dele será copiado.

## 2. Fatos do código que condicionam o desenho

| Fato | Consequência |
|---|---|
| `Contact` **não tem campo de e-mail** (só telefone e nome); e-mail existe em `User` | Precisa de endereços por contato antes de vincular mensagens |
| Segredos já são cifrados com `internal/crypto` (`Encrypt`/`Decrypt`, chave `EncryptionKey`) | Reutilizar para senhas e tokens das caixas |
| Instância única; processos de fundo iniciados em `cmd/whatomate/main.go` | O sincronizador vive no mesmo processo, sem fila externa |
| RBAC por `recurso:ação`, com `view_all` e `view_team` (conversas, vendas) | Mesmo padrão para `mail` |
| Realtime por WebSocket (`BroadcastToOrg`, `BroadcastToAuthorizedViewers`) | O novo evento reaproveita o hub |
| Anexos e uploads em `Storage.LocalPath` | Anexos de e-mail seguem o mesmo armazenamento |

## 3. Escopo do MVP (leitura e resposta)

1. Conectar **uma caixa IMAP por organização** (host, porta, usuário, senha de
   aplicativo), com teste de conexão e senha cifrada.
2. Sincronizar INBOX (e Enviados) por UID, com IDLE e reconexão; corpo em texto e
   HTML, anexos, cabeçalhos de encadeamento.
3. Lista por encadeamento (`Message-ID`, `In-Reply-To`, `References`), leitura,
   **responder** e **nova mensagem** via SMTP.
4. **Vínculo com contato** pelo endereço; a linha do tempo do contato mostra os
   e-mails junto com o WhatsApp.
5. Permissões e auditoria.

Fora do MVP: OAuth (Gmail/Microsoft), várias caixas por usuário, busca de texto
completo, rascunhos sincronizados, regras, modelos, assinatura rica.

## 4. Modelo de dados (migrations aditivas)

- `mail_accounts`: `id, organization_id, name, email_address, imap_host,
  imap_port, smtp_host, smtp_port, username, password_encrypted, tls_mode,
  is_active, last_synced_at, last_error, created_by`.
- `mail_messages`: `id, organization_id, account_id, uid, uid_validity, folder,
  message_id, in_reply_to, references[], thread_id, from_addr, to_addrs[],
  cc_addrs[], subject, snippet, body_text, body_html_sanitized, direction,
  received_at, is_read, has_attachments, contact_id` (nulo se sem vínculo).
  Índices: `(account_id, folder, uid_validity, uid)` único;
  `(organization_id, thread_id, received_at)`;
  `(organization_id, contact_id, received_at)`.
- `mail_attachments`: `id, message_id, filename, mime_type, size, storage_path`.
- `contact_emails`: `id, organization_id, contact_id, address` (normalizado, em
  minúsculas), `is_primary`. Índice único `(organization_id, address)`.
  Decisão: **tabela própria**, porque uma pessoa tem vários endereços e vários
  contatos podem existir por telefone (já é assim com telefones).

## 5. Sincronização e envio

- **Bibliotecas** (todas MIT, já avaliadas em `docs/research/`):
  `emersion/go-imap` v2 (IMAP; a v2 ainda está em beta), `jhillyerd/enmime`
  (MIME) e `wneessen/go-mail` (SMTP). Este documento não adiciona dependência.
- Um trabalhador por caixa ativa: `UIDVALIDITY` guardado; se mudar, ressincroniza
  a pasta; `IDLE` com varredura periódica de reserva; *backoff* exponencial em
  falha, com `last_error` visível ao administrador.
- Idempotência: a chave `(account, folder, uid_validity, uid)` impede duplicar na
  reconexão ou no reinício.
- Envio: SMTP com `Message-ID` próprio, `In-Reply-To` e `References`
  preenchidos, e cópia gravada em Enviados para manter o encadeamento.
- Concorrência: instância única hoje. O trabalhador pega um *advisory lock* por
  caixa (`pg_try_advisory_lock`), então uma segunda instância não duplica.

## 6. Segurança (requisitos, não opções)

- Senhas cifradas com `internal/crypto`; nunca em log nem na API (a resposta
  devolve só um indicador "configurada").
- TLS com verificação de certificado ligada; `tls_mode` explícito
  (STARTTLS ou TLS).
- **HTML de e-mail é não confiável**: sanitizar no servidor (lista de permissão)
  e renderizar em `iframe` com `sandbox`, sem script e com imagens remotas
  bloqueadas até o agente permitir (evita rastreio).
- Anexos: limite de tamanho, tipo MIME validado, nome saneado, download com
  `Content-Disposition: attachment`.
- Isolamento: toda consulta filtra `organization_id`; um e-mail só é visível a
  quem pode ver o contato ou a conversa vinculada, ou tem `mail:view_all`.

## 7. Permissões

Novo recurso `mail`: `read`, `write` (enviar), `manage` (configurar caixas),
`view_all` e `view_team`. Nenhum papel recebe a permissão automaticamente; o
administrador atribui. Mensagem sem contato vinculado só aparece com `view_all`.

## 8. Interface

- Item "E-mail" no menu: lista de encadeamentos, painel de leitura e composição,
  no padrão visual do shell atual.
- Aba "E-mail" no detalhe do contato, com eventos na linha do tempo.
- Configuração em Configurações, Integrações, E-mail.
- Tempo real: evento WebSocket `mail_new_message` com contato e encadeamento.

## 9. Fases e critérios de aceite

| Fase | Entrega | Aceite |
|---|---|---|
| 0 | `contact_emails`, edição e importação | Contato com vários endereços; busca por endereço |
| 1 | Conta IMAP/SMTP e sincronização da INBOX | Reconectar ou reiniciar não duplica; `UIDVALIDITY` tratado; erro visível |
| 2 | Leitura, encadeamento, vínculo com contato, HTML seguro | Mesmo encadeamento em 3 clientes diferentes; HTML sem script nem imagem remota |
| 3 | Resposta e nova mensagem via SMTP, Enviados | A resposta aparece encadeada no cliente do destinatário |
| 4 | Permissões, auditoria, tempo real | `view_team` não vê caixa de outro time; evento chega ao painel |
| 5 | OAuth Gmail e Microsoft | Consentimento e renovação de token funcionando; ver riscos |

Estimativa (minha, não medida): fases 0 a 4 em 2 a 4 semanas de uma pessoa; a
fase 5 é independente e a mais incerta.

## 10. Riscos e decisões em aberto

- **OAuth do Gmail**: os escopos de leitura de e-mail são restritos e exigem
  verificação do aplicativo pelo Google (prazo e custo fora do nosso controle).
  Até lá, senha de aplicativo ou IMAP. Decisão: começar sem OAuth?
- **Microsoft 365** vem desligando a senha básica em IMAP e pode forçar OAuth
  cedo. Verificar com o tenant real antes da fase 1.
- **Caixa compartilhada ou pessoal?** O MVP assume uma caixa por organização
  (por exemplo `atendimento@`). Caixas pessoais mudam o modelo de visibilidade.
- **Volume**: a primeira sincronização grande precisa de limite de histórico
  (por exemplo, últimos 90 dias), configurável.
- **LGPD**: e-mail contém dado pessoal; definir retenção e exclusão por contato.
- **go-imap v2 em beta**: fixar a versão e cobrir com teste contra um servidor
  IMAP de teste.
- **Mensagens sem contato**: reter só para `view_all` ou criar o contato
  automaticamente? Proposta: reter, sem criar contato.
