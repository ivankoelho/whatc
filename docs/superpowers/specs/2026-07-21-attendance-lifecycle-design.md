# Ciclo de Vida do Atendimento — Ciclo 1

**Data:** 2026-07-21
**Branch:** `feature/attendance-lifecycle`
**Escopo:** Ciclo 1 de 2. O Ciclo 2 (visibilidade estrita + escopo de equipe + RBAC) tem spec próprio.

## Objetivo

Ao encerrar um atendimento — manualmente, por SLA ou por inatividade — o contato deve ficar **livre**: sem agente responsável, disponível para um novo ciclo de atendimento. Hoje ele permanece preso ao último agente indefinidamente.

## Diagnóstico da implementação atual

### Modelo de dados

Não existe entidade "Conversa". Uma conversa é a combinação de um `Contact` com uma `AgentTransfer` ativa.

| Conceito | Onde vive |
|---|---|
| Fila | `AgentTransfer` com `status='active'` e `agent_id IS NULL` |
| Atendimento em curso | `AgentTransfer.AgentID` |
| Carteira do cliente | `Contact.AssignedUserID` |
| Equipe | `Team`, `TeamMember` (`TeamRole` manager/agent), `AgentTransfer.TeamID` |
| Encerramento | `TransferStatus`: `active` → `resumed` (manual) ou `expired` (SLA) |

**Os dois campos de atribuição têm significados diferentes e ciclos de vida diferentes.** `AgentTransfer.AgentID` responde "quem está atendendo agora"; `Contact.AssignedUserID` responde "de quem é este cliente". O `ReturnAgentTransfersToQueue` (`internal/handlers/agent_transfers.go:1403`) já os distingue com cuidado: só limpa o segundo quando ele aponta para o agente que saiu, para não apagar uma carteira definida manualmente. Qualquer mudança aqui precisa preservar essa distinção.

### O que já funciona

| Fluxo | Estado |
|---|---|
| Transferir para equipe (fila) | Completo — `createTransferToTeam`, `PickNextTransfer`, `AllowQueuePickup` |
| Transferir para agente | Completo — valida `IsAvailable` |
| Atribuir agente | `AssignAgentTransfer` (atendimento), `AssignContact` (carteira) |
| Devolver à fila quando o agente fica ausente | `ReturnAgentTransfersToQueue`, disparado em `users.go:967` |
| Reabertura por nova mensagem | Funciona — sem transferência ativa, o chatbot reassume |

### Inconsistências encontradas

1. **Três caminhos de encerramento, nenhum libera o contato.** `ResumeFromTransfer` e o auto-close do SLA (`sla_processor.go:139`) mudam o status da transferência e nunca tocam em `Contact.AssignedUserID`.
2. **`contact_status='resolved'` também não libera.** O botão "Concluir atendimento" fecha o status e mantém o agente preso.
3. **A inatividade nunca encerra atendimento humano.** `processClientInactivity` pula contatos com transferência ativa (`sla_processor.go:479`), e a própria consulta os exclui — ver a seção seguinte.
4. **`AssignToSameAgent` produz carteira permanente por omissão.** Ela apenas *grava* `assigned_user_id` na criação da transferência (`agent_transfers.go:520`); como nada limpa o campo, o efeito é permanência. **Não existe "lógica de persistência" a remover** — a correção é aditiva (passar a limpar), não uma deleção. Isso reduz o risco: não estamos retirando comportamento de que alguém dependa.
5. **O rótulo do auto-close descreve um cálculo que não é o implementado** — ver "Rótulos".

### Premissas do pedido que não batem com o código

Registradas para evitar retrabalho no Ciclo 2:

- **`scope_teams_only` não existe.** Zero ocorrências em Go, Vue ou TS. Equipes servem só ao roteamento de transferência; nenhuma consulta de contato consulta participação em equipe. O Ciclo 2 precisa **criar** escopo de equipe, não integrar-se a um existente.
- **`CurrentConversationOnly` ("Agentes veem apenas a conversa atual") não controla visibilidade de conversas.** É usada num único ponto (`contacts.go:307`, dentro de `GetMessages`) para truncar o **histórico de mensagens** à sessão atual do chatbot. Esconde mensagens dentro de uma conversa que o agente já vê. Quem esconde conversas é `scopeAssignedContact` (`contacts.go:208`), apoiado na permissão `contacts:read`.
- **O RBAC não tem conceito de supervisor.** Permissões são planas (`recurso:ação`), com papéis customizáveis e super-admin. `TeamMember.Role` tem `manager`, mas nada consulta esse valor para visibilidade.

## Decisões

| Questão | Decisão | Motivo |
|---|---|---|
| Encerramento vs. `AssignToSameAgent` | Encerrar sempre libera | Alinha com o modelo pedido e com o padrão de fila do mercado |
| Configuração de inatividade | Reaproveitar `ClientInactivity`, um só par de tempos | Instrução explícita de não criar lógica paralela; zero coluna nova |
| Rótulo do auto-close | Corrigir o texto, manter o cálculo | Zero risco de comportamento; a tela deixa de mentir |
| `AssignToSameAgent` | Mantida, com significado corrigido | Continua útil *dentro* de um atendimento aberto (SAC → Logística e volta) |

## Arquitetura proposta

### 1. `releaseContact` — ponto único de liberação

Novo helper em `internal/handlers/contact_status.go`, ao lado do `transitionContactStatus` já em produção:

```go
// releaseContact frees a contact at the end of an attendance: it clears the
// relationship manager, marks the conversation resolved and broadcasts the
// change. actorID is the user who closed the attendance, or nil for automatic
// closes (SLA, inactivity). Idempotent — releasing an already-free contact is
// a no-op.
func (a *App) releaseContact(contact *models.Contact, actorID *uuid.UUID, reason string) error
```

Comportamento:
1. Limpa `Contact.AssignedUserID`
2. Chama `transitionContactStatus(contact, ContactStatusResolved, nil, actorID)` — reaproveita o UPDATE condicional, o audit e o broadcast que já existem
3. Publica `contact_update` via WebSocket para a sidebar refletir a liberação

O `actorID` é repassado porque o audit log só registra transições com ator (`AuditLog.UserID` é `NOT NULL`): sem ele, um encerramento manual perderia o rastro de quem encerrou.

**Ordem no caminho do botão "Concluir atendimento".** `UpdateContactStatus` hoje já chama `transitionContactStatus` com o ator. Ele passa a chamar apenas `releaseContact`, que faz a transição internamente — não os dois. Chamar ambos seria inofensivo (a segunda transição vira no-op, porque `oldStatus == to` retorna `false` antes de qualquer escrita), mas a duplicação confundiria a leitura.

Chamado dos **quatro** caminhos de encerramento:

| Caminho | Arquivo |
|---|---|
| Encerramento manual pelo agente | `ResumeFromTransfer` (`agent_transfers.go:626`) |
| SLA estourado | `autoCloseExpiredTransfers` (`sla_processor.go:139`) |
| Inatividade | novo trecho em `processClientInactivity` |
| Botão "Concluir atendimento" | `UpdateContactStatus` quando vai para `resolved` |

Concentrar num helper evita a alternativa descartada (hook `AfterUpdate` no GORM), que dispararia fora de encerramentos e não saberia o motivo.

### 2. Inatividade cobrindo atendimento humano

**O problema de âncora.** A passada atual seleciona por `chatbot_last_message_at IS NOT NULL` e mede a partir desse campo. Mas `ClearContactChatbotTracking` zera esse campo quando o contato vai para atendimento humano (`sla_processor.go:596`, chamado em `ResumeFromTransfer` e na transferência). Logo, **atendimentos humanos nem entram no laço** — remover o `continue` da linha 479 não basta.

**Solução.** `processClientInactivity` ganha uma segunda seleção, para contatos **com** transferência ativa, ancorada em `Contact.LastMessageAt` (última mensagem em qualquer direção — é o que "sem interação" significa numa conversa entre pessoas). Os tempos e as mensagens são os mesmos de `ClientInactivity`; muda apenas o campo de referência e o que acontece ao encerrar:

1. envia `ClientInactivity.AutoCloseMessage` ao cliente
2. encerra a transferência (`status='expired'`, nota de auditoria)
3. chama `releaseContact`

O flag de lembrete reaproveita a coluna `chatbot_reminder_sent`. O campo Go e o comentário passam a nomeá-la de forma neutra (`InactivityReminderSent`), mantendo `column:chatbot_reminder_sent` — **sem migração**, sem coluna nova.

### 3. Conversa iniciada por agente abre atendimento

**Bug em produção, diagnosticado nesta sessão.** Um agente envia um template para iniciar conversa; o cliente responde; o fluxo do chatbot assume e sequestra a conversa.

**Causa raiz — não é específica de template.** O único guarda contra o bot assumir uma mensagem recebida é `hasActiveAgentTransfer` (`chatbot_processor.go:199`). O sistema modela "tem gente cuidando desta conversa" exclusivamente como uma `AgentTransfer` ativa, e transferências são criadas em apenas três lugares, **todos do lado do chatbot**: o nó de transferência do fluxo (`chatbot_graph_runner.go:728-735`), o caso de chatbot desabilitado (`chatbot_processor.go:216`) e o endpoint manual.

**Nenhum caminho de envio de agente cria transferência.** Logo, uma conversa iniciada por agente não deixa registro de atendimento, e quando o cliente responde o bot assume. Vale igualmente para mensagem livre dentro da janela de 24h — o template só é o caso mais comum porque fora da janela ele é obrigatório.

**Correção.** No `SendOutgoingMessage`, quando `opts.SentByUserID != nil` e o contato não tem transferência ativa, criar:

```go
models.AgentTransfer{
    Status:  models.TransferStatusActive,
    Source:  models.TransferSourceAgentInitiated, // constante nova
    AgentID: opts.SentByUserID,                   // quem falou é quem atende
    ...
}
```

e cancelar a sessão de chatbot ativa, exatamente como o nó de transferência já faz. Intervenção humana ganha do robô.

`TransferSourceAgentInitiated = "agent_initiated"` entra junto das quatro fontes existentes. Como o atendimento já nasce com agente, chama-se `UpdateSLAOnPickup` na criação.

**Por que o gatilho é seguro.** `opts.SentByUserID` é preenchido em exatamente três handlers, todos da interface do agente: `SendMessage` (`contacts.go:686`), `SendMedia` (`contacts.go:871`) e `SendTemplateMessage` (`messages.go:976`). Campanhas em massa **não** passam pelo `SendOutgoingMessage` — o worker chama o cliente WhatsApp diretamente (`internal/worker/worker.go:301`) — e envios do chatbot passam sem `SentByUserID`. Não há risco de abrir um atendimento por destinatário de campanha.

**Relação com o resto deste ciclo.** Sem esta correção, a liberação no encerramento agrava o problema em vez de resolvê-lo: encerra → contato livre → agente envia template depois → cliente responde → bot sequestra. As duas mudanças precisam entrar juntas.

### 4. Remover atribuição como operação explícita

`POST /api/transfers/{id}/unassign`, reaproveitando a lógica já testada de `ReturnAgentTransfersToQueue`: limpa `AgentTransfer.AgentID`, devolve à fila da equipe e limpa `Contact.AssignedUserID` **apenas** se ele apontar para o agente removido. Exige permissão de escrita em transferências.

Com isso as quatro operações do modelo conceitual existem: transferir para equipe, transferir para agente, atribuir, remover atribuição.

### 5. Rótulos e semântica das configurações

| Configuração | Texto atual | Texto novo | Motivo |
|---|---|---|---|
| `AllowQueuePickup` | "Permitir que agentes peguem da fila" | "Permitir que agentes assumam atendimentos da fila" | Clareza; comportamento inalterado |
| `ClientInactivity.AutoCloseMinutes` | "Fechar automaticamente após (minutos)" | "Encerrar após (minutos) de inatividade" | O cálculo mede da última mensagem, não a partir do lembrete |
| `AssignToSameAgent` | "Atribuir ao mesmo agente" | "Manter o mesmo agente durante o atendimento" + nota de que a atribuição não sobrevive ao encerramento | Sem isso, a opção contradiz a nova regra |

Validação nova no formulário: o tempo de encerramento deve ser maior que o do lembrete. Hoje nada impede configurar o inverso, o que faz o lembrete nunca ser enviado.

## Migração de dados

**Nenhuma.** Sem coluna nova, sem alteração de tipo, sem backfill.

Contatos hoje presos a um agente são liberados **no próximo encerramento**, não em massa. Uma liberação retroativa em massa mudaria a carteira de clientes de organizações que operam deliberadamente nesse modelo, sem que ninguém tenha pedido.

## Riscos de compatibilidade

| Risco | Mitigação |
|---|---|
| Organizações que usam carteira fixa perdem a permanência do agente | É a decisão de produto explícita. O texto da configuração passa a dizer a verdade; o campo continua governando o comportamento *dentro* do atendimento |
| Inatividade passa a encerrar atendimento humano que antes ficava aberto | Só age quando `AutoCloseMinutes > 0`, que já é opt-in por organização |
| `releaseContact` limpar carteira definida manualmente | Ele roda no encerramento do atendimento, onde a liberação é justamente o comportamento pedido. O `unassign` (item 3) preserva a regra mais conservadora do `ReturnAgentTransfersToQueue` |

## Arquivos alterados

```
internal/handlers/contact_status.go       releaseContact; chamada no resolved
internal/handlers/messages.go             abre atendimento em envio de agente
internal/models/constants.go              TransferSourceAgentInitiated
internal/handlers/agent_transfers.go      ResumeFromTransfer; novo UnassignTransfer
internal/handlers/sla_processor.go        auto-close libera; inatividade cobre transferências
internal/models/chatbot.go                renomeia o campo Go do flag de lembrete
cmd/whatomate/main.go                     rota POST /api/transfers/{id}/unassign
frontend/src/views/settings/…Chatbot…     rótulos e validação do formulário
frontend/src/i18n/locales/{en,pt-BR}.json strings
+ testes nos pacotes tocados
```

## Testes

**Go**
- `releaseContact`: limpa a atribuição, marca resolvido, publica evento, é idempotente
- Cada um dos quatro caminhos de encerramento libera de fato o contato
- Inatividade: encerra atendimento humano parado além do tempo; **não** encerra conversa com mensagem recente; respeita `AutoCloseMinutes = 0`
- `unassign`: devolve à fila, preserva carteira manual de outro agente, exige permissão
- Envio de agente sem atendimento ativo abre transferência com ele como responsável e cancela a sessão de chatbot
- Envio de agente com atendimento já ativo **não** abre um segundo
- Envio do chatbot (`SentByUserID == nil`) e envio de campanha **não** abrem atendimento
- Regressão do bug relatado: agente envia template → cliente responde → o fluxo **não** dispara
- Ciclo completo: atribui → encerra → nova mensagem do cliente → contato livre, sem agente, chatbot no comando

**Playwright**
- Concluir atendimento e verificar que o contato deixa de exibir agente responsável

## Fora do Ciclo 1

- Visibilidade estrita, permissão `contacts:view_all`, escopo de equipe (Ciclo 2)
- Rótulo/semântica de `CurrentConversationOnly` (Ciclo 2 — é sobre histórico de mensagens, não sobre conversas)
- Tags automáticas no nó de transferência
- Validação e tag obrigatória na criação manual de contato
- Timestamps das notas em pt-BR
