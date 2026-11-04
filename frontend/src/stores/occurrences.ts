import { defineStore } from 'pinia'
import { ref } from 'vue'
import { toast } from 'vue-sonner'
import { i18n } from '@/i18n'
import { sendOutcome, type SendOutcome } from '@/lib/occurrence-customer-messaging'
import { useAuthStore } from '@/stores/auth'
import { loadRecord, saveRecord, clearRecord, recordStorageKey, textHash, answerClosesIntention, ProtocolSendPendingError, IN_FLIGHT_GRACE_MS, type SendIntentRecord, type RecoveryScope, type SendState } from '@/lib/protocol-send-recovery'
import { occurrencesService, occurrenceProcessesService, type Occurrence, type OccurrenceStage, type OccurrenceEvent, type OccurrenceDocumentInput } from '@/services/api'

export const useOccurrencesStore = defineStore('occurrences', () => {
  const occurrences = ref<Occurrence[]>([])
  const total = ref(0)
  const contactOccurrences = ref<Occurrence[]>([])
  const stages = ref<OccurrenceStage[]>([])
  const events = ref<OccurrenceEvent[]>([])
  const isLoading = ref(false)

  async function fetchStages() {
    const res = await occurrencesService.listStages()
    stages.value = res.data.data.stages
  }

  // Returns undefined for a stage that is no longer listed (removed after the
  // occurrence was created), so the badge falls back to its default outline.
  function stageColor(stageID: string): string | undefined {
    return stages.value.find(s => s.id === stageID)?.color
  }

  async function fetchOccurrences(params?: Record<string, string>) {
    isLoading.value = true
    try {
      const res = await occurrencesService.list(params)
      occurrences.value = res.data.data.occurrences
      total.value = res.data.data.total
    } finally {
      isLoading.value = false
    }
  }

  // O quadro carrega cada coluna de forma independente e em paralelo, então
  // isto devolve a página em vez de escrever o array `occurrences`, que a
  // lista possui. Se as colunas escrevessem lá, a última resposta a chegar
  // apagaria as outras.
  async function fetchColumn(params: Record<string, string>) {
    const res = await occurrencesService.list(params)
    return { occurrences: res.data.data.occurrences, total: res.data.data.total }
  }

  async function fetchContactOccurrences(contactId: string) {
    const res = await occurrencesService.listForContact(contactId)
    contactOccurrences.value = res.data.data.occurrences
  }

  async function fetchEvents(occurrenceId: string) {
    const res = await occurrencesService.listEvents(occurrenceId)
    events.value = res.data.data.events
  }

  async function createOccurrence(payload: {
    contact_id: string
    title?: string
    description?: string
    priority?: 'low' | 'normal' | 'high' | 'urgent'
    source_transfer_id?: string
    team_id?: string
    order_ref?: string
    unit_id?: string
    department_id?: string
    category_id?: string
    what_happened_id?: string
    process_id?: string
    sale_channel?: string
    invoice_number?: string
    purchase_date?: string
    product_description?: string
    internal_note?: string
    documents?: OccurrenceDocumentInput[]
  }) {
    const res = await occurrencesService.create(payload)
    // A forwarding agent may not read this conversation: the refresh is best-effort.
    try { await fetchContactOccurrences(payload.contact_id) } catch { /* no access to the contact panel */ }
    return res.data.data
  }

  // Suggested registration text for the agent to review; never sent by this call.
  async function previewRegistrationMessage(occurrenceId: string, processId?: string) {
    if (!processId) return { content: '', hasTemplate: false, isFallback: false }
    try {
      const res = await occurrenceProcessesService.previewMessage(processId, 'registration', occurrenceId)
      const { content, has_template, is_fallback } = res.data.data
      return { content, hasTemplate: has_template, isFallback: is_fallback }
    } catch {
      // The protocol already exists; the agent can still write a message or send the default text.
      toast.error(i18n.global.t('occurrences.previewMessageFailed'))
      return { content: '', hasTemplate: false, isFallback: false }
    }
  }

  // false = the 24h service window is closed (HTTP 422): an expected outcome the
  // UI shows plainly (spec 16/17), not an error to throw past the caller.
  // An empty message makes the backend send the legacy protocol text.
  // 'uncertain' = the provider did not confirm it (HTTP 202): it may have been delivered, so it
  // is neither a success nor a failure, and it is never sent again from here.
  async function sendRegistrationMessage(occurrenceId: string, message: string): Promise<'sent' | 'window_closed' | 'uncertain'> {
    try {
      return sendOutcome(await postProtocol(occurrenceId, message.trim() || undefined))
    } catch (e: any) {
      if (e?.response?.status === 422) return 'window_closed'
      throw e
    }
  }

  // One key per intention. It is kept (in memory AND in the browser, scoped by organization, user and
  // case) while the outcome is unknown: no answer at all, or 202. A double click, a retry, a refresh or
  // coming back later then ask about the SAME send instead of making a second one. Any answer that
  // closes the intention (sent, refused) drops it, and the next click is a new intention, new key.
  const auth = useAuthStore()
  const mem = new Map<string, SendIntentRecord>()
  const scopeOf = (occurrenceId: string): RecoveryScope => ({ organizationId: auth.organizationId, userId: auth.user?.id ?? '', occurrenceId })
  const currentRecord = (occurrenceId: string) => loadRecord(scopeOf(occurrenceId)) ?? mem.get(recordStorageKey(scopeOf(occurrenceId))) ?? null
  function keepRecord(occurrenceId: string, rec: SendIntentRecord) {
    mem.set(recordStorageKey(scopeOf(occurrenceId)), rec)
    saveRecord(scopeOf(occurrenceId), rec)
  }
  function dropRecord(occurrenceId: string) {
    mem.delete(recordStorageKey(scopeOf(occurrenceId)))
    clearRecord(scopeOf(occurrenceId))
  }

  async function postProtocol(occurrenceId: string, message: string | undefined) {
    const h = textHash(message)
    let rec = currentRecord(occurrenceId)
    // A record that is still here is an unresolved intention. A different text must not take a new key
    // over it: that would be a second message while the first may be in flight or delivered.
    if (rec && rec.textHash !== h) throw new ProtocolSendPendingError()
    // A record that exists means this POST repeats an earlier request under the same key. The clock of the
    // in-flight grace restarts with every request: the new one may still be on its way.
    const isRetry = !!rec
    rec = rec ? { ...rec, savedAt: Date.now() } : { key: crypto.randomUUID(), textHash: h, savedAt: Date.now() }
    keepRecord(occurrenceId, rec) // before the request: a refresh mid-flight must still find the key
    try {
      const res = await occurrencesService.sendProtocol(occurrenceId, message, rec.key)
      if (res.status === 202) keepRecord(occurrenceId, { ...rec, sendId: res.data?.data?.send_id ?? rec.sendId })
      else dropRecord(occurrenceId)
      return res.data
    } catch (e: any) {
      if (answerClosesIntention(e, isRetry)) dropRecord(occurrenceId) // only the application's own conclusive answer ends the intention (not a proxy 5xx, a timeout, or a refusal of a retry)
      throw e
    }
  }

  // Asks the server what became of a send (after a refresh, a timeout or a lost answer).
  async function getProtocolSend(occurrenceId: string, sendId: string) {
    return (await occurrencesService.getProtocolSend(occurrenceId, sendId)).data.data
  }

  // Is there a send of this case whose outcome this browser has not seen close? The record is the truth:
  // it is dropped only by a conclusive answer, so the screens ask it instead of re-deriving it from an error.
  const hasPendingProtocolSend = (occurrenceId: string) => !!currentRecord(occurrenceId)

  // What happened to the send this browser started for this case and did not see finish.
  // null = nothing pending. Read-only: it only ever asks, never creates a send.
  async function recoverProtocolSend(occurrenceId: string): Promise<SendState | null> {
    const rec = currentRecord(occurrenceId)
    if (!rec) return null
    try {
      const data = rec.sendId
        ? await getProtocolSend(occurrenceId, rec.sendId)
        : (await occurrencesService.getProtocolSendByKey(occurrenceId, rec.key)).data.data
      if (!rec.sendId) keepRecord(occurrenceId, { ...rec, sendId: data.send_id })
      if (data.state === 'sent' || data.state === 'failed') dropRecord(occurrenceId)
      return data.state
    } catch (e: any) {
      if (e?.response?.status !== 404) throw e // no answer or not allowed: keep the record, say nothing false
      // No claim for the key. A request may still be on its way: until it cannot be, it is "in progress".
      if (!rec.sendId && Date.now() - rec.savedAt < IN_FLIGHT_GRACE_MS) return 'processing'
      dropRecord(occurrenceId)
      return null
    }
  }

  async function changeStage(occurrenceId: string, stageId: string) {
    await occurrencesService.changeStage(occurrenceId, stageId)
    await fetchEvents(occurrenceId)
  }

  // O quadro grava a etapa e para por aí. O `changeStage` acima recarrega a
  // timeline porque a tela de detalhe precisa dela; aqui isso seria uma
  // requisição desperdiçada por arrasto e ainda sobrescreveria `events`, que
  // pertence ao detalhe.
  async function moveStage(occurrenceId: string, stageId: string) {
    await occurrencesService.changeStage(occurrenceId, stageId)
  }

  async function addNote(occurrenceId: string, content: string) {
    await occurrencesService.addNote(occurrenceId, content)
    await fetchEvents(occurrenceId)
  }

  // false = the 24h service window is closed (HTTP 422), an expected outcome the
  // caller shows plainly; any other failure is thrown.
  async function reply(occurrenceId: string, content: string): Promise<boolean> {
    try {
      await occurrencesService.reply(occurrenceId, content)
    } catch (e: any) {
      if (e?.response?.status === 422) return false
      throw e
    }
    await fetchEvents(occurrenceId)
    return true
  }

  async function sendProtocol(occurrenceId: string): Promise<SendOutcome> {
    const body = await postProtocol(occurrenceId, undefined)
    await fetchEvents(occurrenceId)
    return sendOutcome(body)
  }

  function clear() {
    occurrences.value = []
    total.value = 0
    contactOccurrences.value = []
    events.value = []
  }

  return {
    occurrences, total, contactOccurrences, stages, events, isLoading,
    fetchStages, stageColor, fetchOccurrences, fetchColumn, fetchContactOccurrences, fetchEvents,
    createOccurrence, changeStage, moveStage, addNote, reply, sendProtocol,
    previewRegistrationMessage, sendRegistrationMessage, getProtocolSend, recoverProtocolSend, hasPendingProtocolSend, clear,
  }
})
