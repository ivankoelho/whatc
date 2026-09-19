# SAC Processo/Motivo — PR2: Abertura do Protocolo (Frontend) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the "Abrir protocolo" form process-aware: selecting "O que aconteceu" resolves the Processo/Motivo, shows internal guidance and the evidence checklist, marks the process's required fields, and replaces the auto-sent protocol message with a suggest → review → edit → send flow.

**Architecture:** Extends `OpenProtocolForm.vue` and `stores/occurrences.ts` in place. No new send path: the edited message goes through the existing `POST /occurrences/{id}/send-protocol`, which PR1 extended with an optional `message` body. No dynamic field renderer: "required fields" only toggles the required marker/validation on the four existing sale-context inputs.

**Tech Stack:** Vue 3 `<script setup lang="ts">`, Pinia, shadcn-vue, vue-i18n, vitest.

**Depends on:** PR1 (`docs/superpowers/plans/2026-09-18-sac-processo-motivo-pr1-backend.md`) merged into `development`, or this branch created from `feature/sac-processo-motivo-pr1`. Endpoints used: `GET /occurrence-processes/resolve`, `GET /occurrence-processes/{id}/messages/{stage}/preview?occurrence_id=`, `POST /occurrences/{id}/process-messages/use`, `POST /occurrences/{id}/send-protocol` (with optional `{message}`), `POST /occurrences` (with optional `process_id`).

## Global Constraints

- Never touch `main`; branch `feature/sac-processo-motivo-pr2` from `development` (after PR1 merges) or from `feature/sac-processo-motivo-pr1`.
- Never auto-send a process message: the agent always sees and can edit the text first (spec §13).
- New user-facing strings need real i18n keys in every file under `frontend/src/locales/` — no hardcoded literals. Copy the surrounding `occurrences.*` key style.
- The preview endpoint returns `{content, has_template}`. `has_template: false` with empty `content` means "no message registered for this stage" — the UI must say so and let the agent type manually; it must never send blank text or invent a generic message.
- A protocol opened with no resolvable process must behave exactly as today, except the registration message is now reviewed before sending instead of auto-sent.
- Verify UI changes in the browser (`preview_start`) before reporting done; type-checks and unit tests do not prove the form works.

---

### Task 1: Service, types, pure helper + test

**Files:**
- Modify: `frontend/src/services/api.ts`
- Create: `frontend/src/lib/occurrence-process.ts`
- Test: `frontend/src/lib/occurrence-process.spec.ts`

**Interfaces:**
- Produces: `OccurrenceProcess`, `OccurrenceProcessMessage` interfaces; `occurrenceProcessesService.{list,resolve,create,update,delete,listMessages,upsertMessage,previewMessage,logMessageUse}`; `occurrencesService.sendProtocol(id, message?)`; `Occurrence.process_id?/process_name?`; `occurrencesService.create` accepts `process_id?`; `isProcessFieldRequired(process, key)`, `missingProcessFields(process, values)`.

- [ ] **Step 1: Write the failing helper test**

```ts
// frontend/src/lib/occurrence-process.spec.ts
import { describe, it, expect } from 'vitest'
import { isProcessFieldRequired, missingProcessFields } from './occurrence-process'
import type { OccurrenceProcess } from '@/services/api'

const process: OccurrenceProcess = {
  id: '1', name: 'Avaria', description: '', guidance: '', restrictions: '',
  evidence_checklist: [], required_fields: ['invoice_number', 'product_description'],
  is_active: true, position: 0,
}

describe('isProcessFieldRequired', () => {
  it('is true for a listed key', () => {
    expect(isProcessFieldRequired(process, 'invoice_number')).toBe(true)
  })
  it('is false for an unlisted key', () => {
    expect(isProcessFieldRequired(process, 'sale_channel')).toBe(false)
  })
  it('is false with no process', () => {
    expect(isProcessFieldRequired(null, 'invoice_number')).toBe(false)
  })
})

describe('missingProcessFields', () => {
  it('returns required keys whose value is blank', () => {
    expect(missingProcessFields(process, { invoice_number: '  ', product_description: 'Piso' }))
      .toEqual(['invoice_number'])
  })
  it('returns [] when nothing is required', () => {
    expect(missingProcessFields(null, {})).toEqual([])
  })
  it('treats an absent key as blank', () => {
    expect(missingProcessFields(process, {})).toEqual(['invoice_number', 'product_description'])
  })
})
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd frontend && npx vitest run src/lib/occurrence-process.spec.ts`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement the helper**

```ts
// frontend/src/lib/occurrence-process.ts
import type { OccurrenceProcess } from '@/services/api'

export function isProcessFieldRequired(process: OccurrenceProcess | null, key: string): boolean {
  return process?.required_fields.includes(key) ?? false
}

export function missingProcessFields(
  process: OccurrenceProcess | null,
  values: Record<string, string | undefined>,
): string[] {
  if (!process) return []
  return process.required_fields.filter((key) => !(values[key] ?? '').trim())
}
```

- [ ] **Step 4: Add types and service to `api.ts`**

In `interface Occurrence` (after `what_happened_name?`): `process_id?: string` and `process_name?: string`. In `occurrencesService.create`'s payload type add `process_id?: string`. Replace `sendProtocol`:

```ts
  sendProtocol: (id: string, message?: string) =>
    api.post<ApiEnvelope<{ sent: boolean; protocol_number: string }>>(
      `/occurrences/${id}/send-protocol`, message ? { message } : undefined),
```

After `occurrenceWhatHappenedService`:

```ts
export interface OccurrenceProcess {
  id: string
  name: string
  description: string
  category_id?: string
  category?: OccurrenceCategory
  what_happened_id?: string
  what_happened?: OccurrenceWhatHappened
  guidance: string
  restrictions: string
  evidence_checklist: string[]
  required_fields: string[]
  response_minutes?: number
  resolution_minutes?: number
  department_id?: string
  is_active: boolean
  position: number
}

export type OccurrenceMessageStage = 'registration' | 'documents' | 'follow_up' | 'forwarding' | 'closing'

export interface OccurrenceProcessMessage {
  id: string
  process_id: string
  stage: OccurrenceMessageStage
  content: string
  is_active: boolean
}

export const occurrenceProcessesService = {
  list: () => api.get<ApiEnvelope<{ processes: OccurrenceProcess[] }>>('/occurrence-processes'),
  resolve: (whatHappenedId: string) =>
    api.get<ApiEnvelope<{ process: OccurrenceProcess | null }>>('/occurrence-processes/resolve', {
      params: { what_happened_id: whatHappenedId },
    }),
  create: (data: Partial<OccurrenceProcess>) => api.post<ApiEnvelope<OccurrenceProcess>>('/occurrence-processes', data),
  update: (id: string, data: Partial<OccurrenceProcess>) =>
    api.put<ApiEnvelope<OccurrenceProcess>>(`/occurrence-processes/${id}`, data),
  delete: (id: string) => api.delete<ApiEnvelope<{ deleted: boolean }>>(`/occurrence-processes/${id}`),
  listMessages: (processId: string) =>
    api.get<ApiEnvelope<{ messages: OccurrenceProcessMessage[] }>>(`/occurrence-processes/${processId}/messages`),
  upsertMessage: (processId: string, stage: OccurrenceMessageStage, data: { content: string; is_active?: boolean }) =>
    api.put<ApiEnvelope<OccurrenceProcessMessage>>(`/occurrence-processes/${processId}/messages/${stage}`, data),
  previewMessage: (processId: string, stage: OccurrenceMessageStage, occurrenceId: string) =>
    api.get<ApiEnvelope<{ content: string; has_template: boolean }>>(
      `/occurrence-processes/${processId}/messages/${stage}/preview`,
      { params: { occurrence_id: occurrenceId } },
    ),
  logMessageUse: (occurrenceId: string, stage: OccurrenceMessageStage, processName?: string) =>
    api.post<ApiEnvelope<{ logged: boolean }>>(`/occurrences/${occurrenceId}/process-messages/use`, {
      stage, process_name: processName,
    }),
}
```

- [ ] **Step 5: Verify**

Run: `cd frontend && npx vitest run src/lib/occurrence-process.spec.ts` (PASS), then the repo's type-check script (see `frontend/package.json` `scripts`; likely `vue-tsc`). Expected: zero new errors.

- [ ] **Step 6: Commit**

```bash
git checkout -b feature/sac-processo-motivo-pr2 development
git add frontend/src/services/api.ts frontend/src/lib/occurrence-process.ts frontend/src/lib/occurrence-process.spec.ts
git commit -m "feat(sac): process service, types and required-field helper

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 2: `OpenProtocolForm.vue` — resolve process, guidance panel, required markers

**Files:**
- Modify: `frontend/src/components/crm/OpenProtocolForm.vue`
- Modify: every file in `frontend/src/locales/`

- [ ] **Step 1: State and watcher**

Merge `watch` into the existing `vue` import. Add imports `occurrenceProcessesService, type OccurrenceProcess` (into the existing `@/services/api` import) and `isProcessFieldRequired, missingProcessFields` from `@/lib/occurrence-process`. After `whatHappenedOptions`:

```ts
const resolvedProcess = ref<OccurrenceProcess | null>(null)

watch(whatHappenedId, async (id) => {
  resolvedProcess.value = null
  if (!id) return
  try {
    const res = await occurrenceProcessesService.resolve(id)
    // A newer selection may have landed while this request was in flight.
    if (whatHappenedId.value !== id) return
    resolvedProcess.value = res.data.data.process
    // Suggest the category only when the agent hasn't picked one: manual choice wins.
    if (resolvedProcess.value?.category_id && !categoryId.value) {
      categoryId.value = resolvedProcess.value.category_id
    }
  } catch {
    // Resolution is a convenience; failure must not block the form.
    resolvedProcess.value = null
  }
})
```

In `resetForm()` add `resolvedProcess.value = null`.

- [ ] **Step 2: Guidance panel and required markers**

In the case section, after the "O que aconteceu" `<Select>`'s wrapping `</div>` and before the priority block, add a panel with `class="col-span-2 rounded-md border border-blue-500/30 bg-blue-500/5 p-3 space-y-2"` shown `v-if="resolvedProcess"`: title `t('occurrences.processGuidanceTitle')` + process name; `resolvedProcess.guidance` in a `whitespace-pre-line text-xs text-muted-foreground` paragraph; a `t('occurrences.processEvidenceTitle')` heading with a `<ul>` of `evidence_checklist`. Restrictions ("o que não dizer") are internal too: render them in a second block titled `t('occurrences.processRestrictionsTitle')` with a destructive-tinted border, `v-if="resolvedProcess.restrictions"`.

Append `<span v-if="isProcessFieldRequired(resolvedProcess, 'KEY')" class="text-destructive"> *</span>` inside the `<Label>` of: invoice number (`invoice_number`), product (`product_description`), purchase date (`purchase_date`), sale channel (`sale_channel`).

- [ ] **Step 3: Validation and payload**

In `submit()` after the description check:

```ts
  const missing = missingProcessFields(resolvedProcess.value, {
    invoice_number: invoiceNumber.value,
    product_description: productDescription.value,
    purchase_date: purchaseDate.value,
    sale_channel: saleChannel.value,
  })
  if (missing.length > 0) {
    toast.error(t('occurrences.validationProcessFieldsRequired'))
    return
  }
```

Add `process_id: resolvedProcess.value?.id || undefined,` to the `store.createOccurrence({...})` payload, and `process_id?: string` to that action's payload type in `stores/occurrences.ts`.

- [ ] **Step 4: i18n**

Add to each locale's `occurrences` block (Portuguese shown; translate for other locales): `processGuidanceTitle` "Orientação do processo", `processEvidenceTitle` "Documentos a solicitar ao cliente", `processRestrictionsTitle` "O que NÃO dizer ao cliente", `validationProcessFieldsRequired` "Preencha os campos obrigatórios deste processo antes de abrir o protocolo."

- [ ] **Step 5: Browser verification**

`preview_start`, open Abrir protocolo, pick "Produto com Avaria". Confirm: guidance, restrictions and evidence panel render real text; invoice and product show `*`; submitting without them shows the toast; picking a reason with no process (e.g. "Duvida sobre o Produto") shows no panel and no markers; rapidly switching reasons never leaves a stale panel (the in-flight guard). Screenshot as proof.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/crm/OpenProtocolForm.vue frontend/src/stores/occurrences.ts frontend/src/locales
git commit -m "feat(sac): resolve process on reason, guidance panel, required fields

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 3: Suggest → review → edit → send the registration message

**Files:**
- Modify: `frontend/src/stores/occurrences.ts`, `frontend/src/components/crm/OpenProtocolForm.vue`, `frontend/src/locales/*`
- Check callers: grep the frontend for `trySendProtocol` (the chat's `ContactOccurrencesPanel` and others may call it) before removing it.

**Interfaces:**
- Consumes: `occurrenceProcessesService.previewMessage`/`logMessageUse`, `occurrencesService.sendProtocol(id, message?)`.
- Produces: store actions `previewRegistrationMessage(occurrenceId, processId?) => Promise<{content: string; hasTemplate: boolean}>`, `sendRegistrationMessage(occurrenceId, message) => Promise<boolean>`.

- [ ] **Step 1: Store actions**

Add `occurrenceProcessesService` to the store's `@/services/api` import and add:

```ts
  // Suggested registration text for the agent to review; never sent by this call.
  async function previewRegistrationMessage(occurrenceId: string, processId?: string) {
    if (!processId) return { content: '', hasTemplate: false }
    try {
      const res = await occurrenceProcessesService.previewMessage(processId, 'registration', occurrenceId)
      return { content: res.data.data.content, hasTemplate: res.data.data.has_template }
    } catch {
      return { content: '', hasTemplate: false }
    }
  }

  // false = the 24h window is closed (HTTP 422): an expected outcome to show
  // plainly, same contract trySendProtocol has today.
  async function sendRegistrationMessage(occurrenceId: string, message: string): Promise<boolean> {
    try {
      await occurrencesService.sendProtocol(occurrenceId, message || undefined)
      return true
    } catch (e: any) {
      if (e?.response?.status === 422) return false
      throw e
    }
  }
```

Export both. Keep `trySendProtocol` if grep finds any other caller; delete it only if this form was its sole caller.

- [ ] **Step 2: Form flow**

Change the result type's `sent` to `boolean | null` (`null` = not attempted yet). Replace the tail of `submit()` (from the old `trySendProtocol` call):

```ts
    const preview = await store.previewRegistrationMessage(occurrence.id, resolvedProcess.value?.id)
    suggestedMessage.value = preview.content
    result.value = {
      protocolId: occurrence.id,
      protocolNumber: occurrence.protocol_number,
      title: occurrence.title,
      sent: null,
    }
    emit('created', occurrence.id)
```

Add state and handlers:

```ts
const suggestedMessage = ref('')
const sendingMessage = ref(false)

async function copySuggestedMessage() {
  try {
    await navigator.clipboard.writeText(suggestedMessage.value)
    toast.success(t('occurrences.messageCopied'))
  } catch {
    toast.error(t('occurrences.messageCopyFailed'))
  }
}

async function sendSuggestedMessage() {
  if (!result.value || sendingMessage.value) return
  sendingMessage.value = true
  try {
    const sent = await store.sendRegistrationMessage(result.value.protocolId, suggestedMessage.value)
    result.value = { ...result.value, sent }
    if (sent && resolvedProcess.value) {
      await occurrenceProcessesService.logMessageUse(result.value.protocolId, 'registration', resolvedProcess.value.name)
    }
  } catch (e) {
    toast.error(getErrorMessage(e, t('chat.occurrenceCreateFailed')))
  } finally {
    sendingMessage.value = false
  }
}
```

`resetForm()` also clears `suggestedMessage`. An empty `suggestedMessage` on send falls through to the backend's legacy text (empty `message` → legacy), so a process-less occurrence still sends today's exact protocol text.

- [ ] **Step 3: Template**

In the `v-if="result"` block replace the sent/not-sent `<p>` with: when `result.sent === null`, a left-aligned block with a `Label` (`occurrences.suggestedMessageLabel`), a `Textarea v-model="suggestedMessage" :rows="8"`, a hint paragraph shown only when the preview had no template (`occurrences.noSuggestedMessage`, using a `hasSuggestedTemplate` ref set from `preview.hasTemplate`), and two buttons — outline "Copiar mensagem" and primary "Enviar pelo WhatsApp" (disabled while `sendingMessage`, with the existing `Loader2` spinner). When `result.sent !== null`, keep the existing green/amber status `<p>` unchanged.

- [ ] **Step 4: i18n**

`suggestedMessageLabel` "Mensagem sugerida", `copyMessage` "Copiar mensagem", `sendMessage` "Enviar pelo WhatsApp", `messageCopied` "Mensagem copiada", `messageCopyFailed` "Não foi possível copiar a mensagem", `noSuggestedMessage` "Não há mensagem cadastrada para esta etapa. Escreva uma manualmente ou envie o texto padrão do protocolo."

- [ ] **Step 5: Browser verification (golden path and regressions)**

1. Contact inside the 24h window + process-backed reason: the box shows the real template with `[Nome]`/`[Protocolo]`/`[NF]` already substituted; edit a word; Enviar; confirm via `read_network_requests` that `send-protocol` carried the edited text, and the occurrence timeline gained a `process_message_used` event.
2. No reason selected: the box is empty with the hint; Enviar sends today's legacy text.
3. Contact outside the window: Enviar shows the existing amber "not sent" state.
4. Copiar puts the text on the clipboard.
5. Regression: any other component that used `trySendProtocol` (found by grep) still works.

- [ ] **Step 6: Full check and commit**

Run: `cd frontend && npm run build && npx vitest run` — expected PASS, no new warnings.

```bash
git add frontend/src/stores/occurrences.ts frontend/src/components/crm/OpenProtocolForm.vue frontend/src/locales
git commit -m "feat(sac): review-before-send suggested registration message

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

Open the PR: `feat(sac): Processo/Motivo — abertura do protocolo`.
