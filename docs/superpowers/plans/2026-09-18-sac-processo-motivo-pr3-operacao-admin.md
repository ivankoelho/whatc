# SAC Processo/Motivo — PR3: Operação e Administração Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let agents use stage messages (documents/follow_up/forwarding/closing) from the occurrence detail view, log their use on the existing timeline, and let admins configure processes and their messages without code changes.

**Architecture:** The detail view prefills its *existing* reply textarea from the preview endpoint and sends through the *existing* reply action — no new send path. The admin screen clones the existing settings-screen pattern (`OccurrenceSLAPoliciesView.vue` for upsert-by-key, `OccurrenceCategoriesView.vue` for list + edit).

**Tech Stack:** Vue 3 `<script setup lang="ts">`, Pinia, shadcn-vue, vue-i18n, vitest, Playwright e2e where already present.

**Depends on:** PR1 (backend) and PR2 (`occurrenceProcessesService`, `OccurrenceProcess`/`OccurrenceMessageStage` types, `isProcessFieldRequired`) merged, or this branch created from `feature/sac-processo-motivo-pr2`.

## Global Constraints

- Never touch `main`; branch `feature/sac-processo-motivo-pr3` from `development` (after PR2 merges) or from `feature/sac-processo-motivo-pr2`.
- Never send a message the agent hasn't seen: the picker only prefills the reply box.
- `has_template: false` means "no message registered for this stage": show that plainly (toast) and leave the reply box untouched. Never fill it with empty or generated text.
- The admin screen is gated on `occurrences.processes:read` (route + nav), writes on `:write`, deletes on `:delete` — copy the exact guard shape the sibling settings screens use.
- Required fields in the admin form are a fixed checkbox list of the four existing keys (`invoice_number`, `product_description`, `purchase_date`, `sale_channel`); no generic field-type builder.
- One active process per reason is enforced by the backend with HTTP 409 — the admin form must surface that message, not a generic failure.
- Time values in the admin form are calendar minutes (same semantics as the SLA policies screen); label them as minutes, not business days.
- Verify in the browser before reporting done.

---

### Task 1: Occurrence detail — stage message picker

**Files:**
- Modify: `frontend/src/views/crm/OccurrenceDetailView.vue`, `frontend/src/i18n/locales/{en,pt-BR}.json`

- [ ] **Step 1: Read the view first**

`Read frontend/src/views/crm/OccurrenceDetailView.vue` and note (a) the ref behind the reply `<Textarea>`, (b) the function that sends the reply and where it clears the textarea after success, (c) how the occurrence is held (`occurrence` ref name), (d) whether `process_id` reaches it (`OccurrenceResponse.process_id`, added in PR1). Use those real names below in place of `replyContent`/`occurrence`.

- [ ] **Step 2: Picker state and loader**

```ts
const messageStage = ref<'documents' | 'follow_up' | 'forwarding' | 'closing'>('documents')
const loadingSuggestion = ref(false)
const lastSuggestedStage = ref<OccurrenceMessageStage | null>(null)

async function loadSuggestedMessage() {
  const occ = occurrence.value
  if (!occ?.process_id) return
  loadingSuggestion.value = true
  try {
    const res = await occurrenceProcessesService.previewMessage(occ.process_id, messageStage.value, occ.id)
    if (!res.data.data.has_template) {
      toast.info(t('occurrences.noMessageForStage'))
      return
    }
    replyContent.value = res.data.data.content
    lastSuggestedStage.value = messageStage.value
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.loadSuggestedMessageFailed')))
  } finally {
    loadingSuggestion.value = false
  }
}
```

- [ ] **Step 3: Template**

Directly above the reply `<Textarea>`, `v-if="occurrence?.process_id"`: a flex row with a `Select v-model="messageStage"` (items `documents`, `follow_up`, `forwarding`, `closing`, labelled via `t('occurrences.stageDocuments')` etc.) and an outline `Button` "Carregar mensagem sugerida" bound to `loadSuggestedMessage`, disabled while `loadingSuggestion`. `registration` is deliberately not listed: it is handled at protocol creation (PR2).

- [ ] **Step 4: Log usage after a successful send**

Record use whenever the sent text *started from* a suggestion, even if the agent edited it (spec: "sugerir → revisar → editar → enviar"). So track it with the `lastSuggestedStage` ref set in `loadSuggestedMessage` (drop the `lastSuggestedContent` ref from Step 2; it isn't needed). In the reply-sent success path, before the textarea is cleared:

```ts
if (occurrence.value?.process_id && lastSuggestedStage.value) {
  try {
    await occurrenceProcessesService.logMessageUse(
      occurrence.value.id, lastSuggestedStage.value, occurrence.value.process_name)
  } catch {
    // A logging failure must not undo a reply that already went out.
  }
}
lastSuggestedStage.value = null
```

Also reset `lastSuggestedStage.value = null` if the agent clears the reply box without sending.

- [ ] **Step 5: i18n**

`stageDocuments` "Solicitar documentos", `stageFollowUp` "Acompanhamento", `stageForwarding` "Encaminhamento", `stageClosing` "Encerramento", `loadSuggestedMessage` "Carregar mensagem sugerida", `noMessageForStage` "Não há mensagem cadastrada para esta etapa. Escreva manualmente.", `loadSuggestedMessageFailed` "Não foi possível carregar a mensagem sugerida".

- [ ] **Step 6: Browser verification**

1. Occurrence opened from "Avaria": pick "Solicitar documentos" → box fills with the resolved `documents` text; edit; send via the normal reply button; timeline shows a `reply` event and a `process_message_used` event.
2. Pick "Encerramento" (no seeded template): toast appears, reply box unchanged.
3. Occurrence with no process: picker not rendered; reply works exactly as before.
4. Send a normal (non-suggested) reply: no `process_message_used` event is created.

- [ ] **Step 7: Commit**

```bash

git add frontend/src/views/crm/OccurrenceDetailView.vue frontend/src/i18n/locales
git commit -m "feat(sac): stage message picker prefills the occurrence reply box

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 2: Timeline rendering of `process_message_used`

**Files:**
- Modify: whichever component renders `OccurrenceEvent.type` in the timeline (grep `protocol_sent` and `stage_change` under `frontend/src`), `frontend/src/services/api.ts` (`OccurrenceEvent['type']` union if typed), `frontend/src/i18n/locales/{en,pt-BR}.json`

- [ ] **Step 1: Find the event-type switch**

Grep for `'protocol_sent'` across `frontend/src`. Every place that maps an event type to an icon, label or colour needs a `process_message_used` entry; an unknown type must not render as a raw key.

- [ ] **Step 2: Add the case**

Add `process_message_used` to the type union in `api.ts` and to each mapping found in Step 1, with a label from a new key `occurrences.eventProcessMessageUsed` ("Mensagem sugerida utilizada") and the same icon style neighbouring events use. The event's `content` (already human-readable from the backend) renders as the detail line.

- [ ] **Step 3: Browser verification**

Open the occurrence from Task 1's verification and confirm the event renders with its label and content, not a raw type string.

- [ ] **Step 4: Commit**

```bash
git add frontend/src
git commit -m "feat(sac): render process_message_used events on the timeline

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 3: Admin settings screen for processes and stage messages

**Files:**
- Create: `frontend/src/views/settings/OccurrenceProcessesView.vue`
- Modify: the router file and settings navigation that register `OccurrenceSLAPoliciesView`/`OccurrenceCategoriesView`; `frontend/src/i18n/locales/{en,pt-BR}.json`

- [ ] **Step 1: Read the precedents, then copy them**

Read `OccurrenceSLAPoliciesView.vue` and `OccurrenceCategoriesView.vue` in full, and grep the router/nav for how they are registered and permission-gated. Reuse their table/dialog/toast/error-handling markup and route-guard shape verbatim instead of writing new shadcn-vue layout from memory.

- [ ] **Step 2: Build the view**

- Table: name, category, reason ("O que aconteceu"), active badge, actions. Loads `occurrenceProcessesService.list()`.
- Edit dialog fields: name, description, category select (`occurrenceCategoriesService.list`), reason select (`occurrenceWhatHappenedService.list`), department select (`departmentsService.list`), guidance textarea, restrictions textarea, evidence checklist (one item per line in a textarea, split/trim/filter on save, joined with newlines on load), required-field checkboxes for the four fixed keys, response and resolution minutes (optional numeric inputs; blank sends `undefined`), active switch.
- On HTTP 409 from create/update, show the backend's message (an active process already exists for that reason) as an inline error in the dialog.
- Messages panel inside the dialog for existing processes: five tabs (`registration`, `documents`, `follow_up`, `forwarding`, `closing`). On open call `listMessages(processId)`; each tab has a content textarea, an active switch and Save → `upsertMessage`. A stage with no row shows an empty textarea and a hint listing the supported variables `[Nome] [Atendente] [Protocolo] [NF] [Prazo] [Loja]`.
- Delete: confirm dialog; on 409 show "in use by existing occurrences" and keep the row.
- All strings under a new `settings.occurrenceProcesses.*` block, shaped like the sibling screens' key blocks.

- [ ] **Step 3: Route, nav, permission gate**

Register the route and nav entry exactly as the siblings do, gated on `occurrences.processes:read`; hide Save/Delete controls without `:write`/`:delete`.

- [ ] **Step 4: Browser verification**

As admin: the five seeded processes list; edit one's guidance, save, reload, change persists; try activating a second process on a reason that already has an active one → 409 message shown; edit the `documents` message of Avaria, then open an Avaria occurrence and confirm the picker loads the edited text; as an agent-role user the nav entry is hidden and direct navigation is denied.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/views/settings/OccurrenceProcessesView.vue frontend/src/router frontend/src/i18n/locales
git commit -m "feat(sac): admin screen for processes and stage messages

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 4: Full regression and final report

**Files:** none (verification task).

- [ ] **Step 1: Automated checks**

Run: `go build ./... && go vet ./... && go test ./... -count=1` then `cd frontend && npm run typecheck && npm run test:unit && npm run i18n:keys && npm run build`. Expected: all PASS. If the repo has a Playwright suite covering occurrences, run its occurrence specs too.

- [ ] **Step 2: Browser regression on untouched paths**

Kanban board renders cards with SLA badges; dragging a card between stages persists and updates a second open tab (websocket); an occurrence created with no process opens, replies and changes stage as before; the five SAC dashboard widgets still show counts, and an occurrence opened from a process appears in "em aberto" and, past its deadline, in "vencidos"; the protocol number sequence continues without gaps.

- [ ] **Step 3: Final report to the user**

Structure: Implementado / Arquivos alterados / Banco / API / Frontend / Testes / Validação / Pendências. The Pendências section must list, as open business decisions awaiting product-owner sign-off and not as settled technical choices:

1. The Category/WhatHappened mapping used for the five seeded processes, including the three reasons created by the seed ("Devolução Após 24h", "Alteração de Pedido em Separação", "Desistência do Pedido"); the validated HTML groups cases by delivery stage, not by the system's Category/WhatHappened vocabulary.
2. The calendar-minutes values derived from prazo text ("5 dias úteis" → 7200 min etc.), which share `OccurrenceSLAPolicy`'s semantics and are not a business-hours calendar.
3. Only `registration` is seeded for all five processes (plus `documents` for Avaria); follow_up/forwarding/closing texts are not in the validated map and were not invented — admins add them through the new screen.

Also state what was deliberately not built (generic custom-field engine, attachments, automatic sending, bot, AI, ERP, BPM map) and that no known regression remains, or list any that does.
