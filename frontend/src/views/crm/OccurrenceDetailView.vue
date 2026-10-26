<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import DetailPageLayout from '@/components/shared/DetailPageLayout.vue'
import { IconButton, DeleteConfirmDialog } from '@/components/shared'
import OccurrenceDocumentsCard from '@/components/crm/OccurrenceDocumentsCard.vue'
import OccurrenceParticipantsCard from '@/components/crm/OccurrenceParticipantsCard.vue'
import OccurrenceCommunicationCard from '@/components/crm/OccurrenceCommunicationCard.vue'
import OccurrenceChatPanel from '@/components/crm/OccurrenceChatPanel.vue'
import OccurrenceWhatsAppCard from '@/components/crm/OccurrenceWhatsAppCard.vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  occurrencesService,
  occurrenceProcessesService,
  type Occurrence,
  type OccurrenceEvent,
  type OccurrenceMessageStage,
} from '@/services/api'
import { useOccurrencesStore } from '@/stores/occurrences'
import { useUsersStore } from '@/stores/users'
import { useAuthStore } from '@/stores/auth'
import { wsService } from '@/services/websocket'
import { formatDateTime } from '@/lib/utils'
import { getErrorMessage } from '@/lib/api-utils'
import {
  ClipboardList,
  Send,
  Loader2,
  PlusCircle,
  StickyNote,
  Reply,
  Lightbulb,
  ArrowRightLeft,
  UserPlus,
  CheckCircle2,
  Copy,
  Trash2,
  MessageCircle,
  ChevronDown,
} from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const store = useOccurrencesStore()
const usersStore = useUsersStore()
const authStore = useAuthStore()

// Sentinel for "no assignee" — the Select component can't carry an empty
// string as an item value, but that's exactly what the backend needs to see
// to clear assigned_user_id (see occurrences.go's resolveAssignee).
const UNASSIGNED = '__unassigned__'

// Old links used ?aba=<tab>. The page is a single column now, so the value only
// says which section to bring into view; the query is then dropped.
const legacySection: Record<string, string> = {
  summary: 'case-summary', customer: 'case-customer', internal: 'case-internal', history: 'case-history', people: 'case-people',
}
function openLegacySection() {
  const aba = route.query.aba
  if (typeof aba !== 'string') return
  const { aba: _drop, ...rest } = route.query
  void router.replace({ query: rest })
  const el = document.getElementById(legacySection[aba] ?? '')
  el?.scrollIntoView?.({ block: 'start' })
  // The customer card loads after the page: scroll again once the layout settles.
  setTimeout(() => el?.scrollIntoView?.({ block: 'start' }), 700)
}

const HISTORY_PREVIEW = 6
const showAllEvents = ref(false)
const shownEvents = computed(() => {
  const all = [...store.events].reverse() // most recent first
  return showAllEvents.value ? all : all.slice(0, HISTORY_PREVIEW)
})

const occurrenceId = computed(() => route.params.id as string)
const occurrence = ref<Occurrence | null>(null)
const isLoading = ref(true)
const isNotFound = ref(false)
const isSendingProtocol = ref(false)
// "Responder ao cliente": text SENT to the contact via WhatsApp (unlike newNoteContent,
// which is internal). The stage-message picker prefills this ref.
const replyContent = ref('')
const isReplying = ref(false)
const replyWindowClosed = ref(false)
// Stage-message picker: prefills replyContent from a process's registered template.
const messageStage = ref<Exclude<OccurrenceMessageStage, 'registration'>>('documents')
const loadingSuggestion = ref(false)
const lastSuggestedStage = ref<OccurrenceMessageStage | null>(null)
const canWrite = computed(() => authStore.hasPermission('occurrences', 'write'))
const isUpdatingAssignee = ref(false)
const isClaiming = ref(false)
const waConfigured = ref(false)
const isDeleteDialogOpen = ref(false)
const isDeleting = ref(false)

const eventIcons: Record<string, any> = {
  opened: PlusCircle,
  note: StickyNote,
  stage_change: ArrowRightLeft,
  assignment: UserPlus,
  protocol_sent: Send,
  closed: CheckCircle2,
  reply: Reply,
  process_message_used: Lightbulb,
  forwarded: Send,
  participant_added: UserPlus,
  participant_removed: UserPlus,
  reopened: ArrowRightLeft,
  scope_regularized: ClipboardList,
  customer_notification: Send,
}

const priorityLabels: Record<string, string> = {
  low: 'occurrences.priorityLow',
  normal: 'occurrences.priorityNormal',
  high: 'occurrences.priorityHigh',
  urgent: 'occurrences.priorityUrgent',
}

const eventLabels: Record<string, string> = {
  opened: 'occurrences.eventOpened',
  note: 'occurrences.eventNote',
  stage_change: 'occurrences.eventStageChange',
  assignment: 'occurrences.eventAssignment',
  protocol_sent: 'occurrences.eventProtocolSent',
  closed: 'occurrences.eventClosed',
  reply: 'occurrences.eventReply',
  process_message_used: 'occurrences.eventProcessMessageUsed',
  forwarded: 'occurrences.eventForwarded',
  participant_added: 'occurrences.eventParticipantAdded',
  participant_removed: 'occurrences.eventParticipantRemoved',
  reopened: 'occurrences.eventReopened',
  scope_regularized: 'occurrences.eventScopeRegularized',
  customer_notification: 'occurrences.eventCustomerNotice',
}

const breadcrumbs = computed(() => [
  { label: t('occurrences.title'), href: '/crm/occurrences' },
  { label: occurrence.value?.protocol_number || '' },
])

const activeUsers = computed(() => usersStore.users.filter(u => u.is_active))
const assigneeSelectValue = computed(() => occurrence.value?.assigned_user_id || UNASSIGNED)

// Atomic on the server: when someone else claimed first this answers 409 and
// the case is reloaded so the new owner shows.
async function claimOccurrence() {
  if (!occurrence.value) return
  isClaiming.value = true
  try {
    await occurrencesService.claim(occurrence.value.id)
    toast.success(t('occurrences.claimed'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.claimFailed')))
  } finally {
    isClaiming.value = false
    await loadOccurrence()
  }
}

async function loadOccurrence() {
  isLoading.value = true
  isNotFound.value = false
  try {
    const res = await occurrencesService.get(occurrenceId.value)
    occurrence.value = res.data.data
  } catch {
    isNotFound.value = true
  } finally {
    isLoading.value = false
  }
}

/** Só reage a eventos da própria ocorrência aberta — o resto é ignorado. */
function handleOccurrenceChanged(payload: Occurrence) {
  if (occurrence.value && payload.id === occurrence.value.id) {
    occurrence.value = payload
  }
}

function handleOccurrenceEventCreated(payload: OccurrenceEvent) {
  if (!occurrence.value || payload.occurrence_id !== occurrence.value.id) return
  // Evita um flash de item duplicado quando esta própria aba é a origem: o
  // submit de nota já recarrega store.events do REST logo em seguida.
  if (store.events.some(e => e.id === payload.id)) return
  store.events.push(payload)
}

let unsubscribeOccurrenceChanged: (() => void) | null = null
let unsubscribeOccurrenceEventCreated: (() => void) | null = null

async function handleStageChange(stageId: string) {
  if (!occurrence.value || stageId === occurrence.value.stage_id) return
  try {
    await store.changeStage(occurrence.value.id, stageId)
    await loadOccurrence()
    toast.success(t('occurrences.stageChanged'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.stageChangeFailed')))
  }
}

async function handleAssigneeChange(value: string) {
  if (!occurrence.value) return
  const newAssigneeId = value === UNASSIGNED ? '' : value
  if ((occurrence.value.assigned_user_id || '') === newAssigneeId) return
  isUpdatingAssignee.value = true
  try {
    await occurrencesService.update(occurrence.value.id, {
      title: occurrence.value.title,
      description: occurrence.value.description,
      priority: occurrence.value.priority,
      assigned_user_id: newAssigneeId,
    })
    await Promise.all([loadOccurrence(), store.fetchEvents(occurrenceId.value)])
    toast.success(t('occurrences.assigneeUpdated'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.assigneeUpdateFailed')))
  } finally {
    isUpdatingAssignee.value = false
  }
}

async function confirmDelete() {
  if (!occurrence.value) return
  isDeleting.value = true
  try {
    await occurrencesService.delete(occurrence.value.id)
    toast.success(t('occurrences.deleteProtocolSuccess'))
    router.push('/crm/occurrences')
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.deleteProtocolFailed')))
  } finally {
    isDeleting.value = false
    isDeleteDialogOpen.value = false
  }
}

function copyProtocol() {
  if (!occurrence.value) return
  navigator.clipboard.writeText(occurrence.value.protocol_number)
  toast.success(t('common.copiedToClipboard'))
}

async function sendProtocol() {
  if (!occurrence.value) return
  isSendingProtocol.value = true
  try {
    await store.sendProtocol(occurrence.value.id)
    toast.success(t('occurrences.protocolSent'))
  } catch (e: any) {
    if (e?.response?.status === 422) {
      toast.error(t('chat.sendProtocolWindowClosed'))
    } else {
      toast.error(getErrorMessage(e, t('occurrences.protocolSendFailed')))
    }
  } finally {
    isSendingProtocol.value = false
  }
}

async function loadSuggestedMessage() {
  const occ = occurrence.value
  if (!occ?.process_id) return
  if (replyContent.value.trim()) {
    toast.info(t('occurrences.replyBoxNotEmpty'))
    return
  }
  loadingSuggestion.value = true
  try {
    const res = await occurrenceProcessesService.previewMessage(occ.process_id, messageStage.value, occ.id)
    if (!res.data.data.has_template || res.data.data.is_fallback) {
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

/** Clears the "window closed" warning as the agent edits, and drops a stale
 * suggestion reference once the box is emptied by hand (not via send). */
function handleReplyInput(value: string) {
  replyWindowClosed.value = false
  if (!value.trim()) lastSuggestedStage.value = null
}

async function submitReply() {
  // isReplying also guards against a double click before the button re-renders as disabled.
  if (!occurrence.value || isReplying.value || !replyContent.value.trim()) return
  isReplying.value = true
  replyWindowClosed.value = false
  try {
    const sent = await store.reply(occurrence.value.id, replyContent.value.trim())
    if (!sent) {
      replyWindowClosed.value = true // text stays in the box
      return
    }
    if (occurrence.value.process_id && lastSuggestedStage.value) {
      try {
        await occurrenceProcessesService.logMessageUse(
          occurrence.value.id, lastSuggestedStage.value, occurrence.value.process_name)
      } catch {
        // A logging failure must not undo a reply that already went out.
      }
    }
    lastSuggestedStage.value = null
    replyContent.value = ''
    toast.success(t('occurrences.replySent'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.replyFailed')))
  } finally {
    isReplying.value = false
  }
}

// Old internal notes stay in the history below; new discussion happens in the internal chat panel.

onMounted(async () => {
  await Promise.all([
    store.fetchStages(),
    loadOccurrence(),
    store.fetchEvents(occurrenceId.value),
    usersStore.users.length === 0 ? usersStore.fetchUsers().catch(() => {}) : Promise.resolve(),
  ])

  unsubscribeOccurrenceChanged = wsService.onOccurrenceChanged(handleOccurrenceChanged)
  unsubscribeOccurrenceEventCreated = wsService.onOccurrenceEventCreated(handleOccurrenceEventCreated)
  await nextTick()
  openLegacySection()
})

onUnmounted(() => {
  unsubscribeOccurrenceChanged?.()
  unsubscribeOccurrenceEventCreated?.()
})
</script>

<template>
  <div class="h-full">
    <DetailPageLayout
      :title="occurrence?.title || ''"
      :icon="ClipboardList"
      icon-gradient="bg-gradient-to-br from-violet-500 to-purple-600 shadow-violet-500/20"
      back-link="/crm/occurrences"
      :breadcrumbs="breadcrumbs"
      full-width
      :is-loading="isLoading"
      :is-not-found="isNotFound"
      :not-found-title="$t('occurrences.notFound')"
    >
      <template #actions>
        <div v-if="occurrence" class="flex w-full flex-wrap items-center gap-2 sm:w-auto">
          <Select :model-value="occurrence.stage_id" :disabled="!!occurrence.can && !occurrence.can.change_status && !occurrence.can.resolve" @update:model-value="handleStageChange($event as string)">
            <SelectTrigger class="w-48">
              <SelectValue :placeholder="$t('occurrences.selectStage')" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="stage in store.stages" :key="stage.id" :value="stage.id">
                {{ stage.name }}
              </SelectItem>
            </SelectContent>
          </Select>
          <Button size="sm" variant="outline" as-child>
            <RouterLink :to="`/chat/${occurrence.contact_id}`">
              <MessageCircle class="h-4 w-4 mr-2 text-emerald-500" aria-hidden="true" />
              {{ $t('chat.openConversation') }}
            </RouterLink>
          </Button>
          <Button v-if="canWrite && !occurrence.assigned_user_id && (occurrence.can?.claim ?? true)" size="sm" data-testid="claim-occurrence" :disabled="isClaiming" @click="claimOccurrence">
            <Loader2 v-if="isClaiming" class="h-4 w-4 mr-2 animate-spin" />
            <UserPlus v-else class="h-4 w-4 mr-2" />
            {{ $t('occurrences.claim') }}
          </Button>
          <Button size="sm" :disabled="isSendingProtocol" @click="sendProtocol">
            <Loader2 v-if="isSendingProtocol" class="h-4 w-4 mr-2 animate-spin" />
            <Send v-else class="h-4 w-4 mr-2" />
            {{ $t('chat.sendProtocol') }}
          </Button>
          <IconButton
            v-if="authStore.user?.is_super_admin"
            :icon="Trash2"
            :label="$t('occurrences.deleteProtocol')"
            class="h-9 w-9 text-destructive hover:text-destructive"
            @click="isDeleteDialogOpen = true"
          />
        </div>
      </template>

      <template v-if="occurrence">
      <div class="mx-auto w-full max-w-[1600px] space-y-6" data-testid="unified-case">

        <!-- 1. Operational strip: what it is, whose it is, how urgent -->
        <section id="case-summary" class="scroll-mt-4 rounded-lg border bg-card px-4 py-3" data-testid="case-strip">
          <div class="flex flex-wrap items-center gap-x-8 gap-y-3 text-sm">
            <div class="flex items-center gap-2">
              <span class="font-mono text-base font-semibold tracking-wide">{{ occurrence.protocol_number }}</span>
              <IconButton :icon="Copy" :label="$t('occurrences.copyProtocol')" class="h-7 w-7" @click="copyProtocol" />
              <Badge variant="outline" :style="{ borderColor: store.stageColor(occurrence.stage_id), color: store.stageColor(occurrence.stage_id) }">
                {{ occurrence.stage_name }}
              </Badge>
            </div>
            <dl class="flex flex-1 flex-wrap items-center gap-x-8 gap-y-2">
              <div>
                <dt class="text-xs text-muted-foreground">{{ $t('occurrences.generalState') }}</dt>
                <dd>{{ $t('occurrences.generalStates.' + (occurrence.general_state || 'new')) }}</dd>
              </div>
              <div v-if="occurrence.unit_name">
                <dt class="text-xs text-muted-foreground">{{ $t('occurrences.unitLabel') }}</dt>
                <dd>{{ occurrence.unit_name }}</dd>
              </div>
              <div v-if="occurrence.team_name">
                <dt class="text-xs text-muted-foreground">{{ $t('occurrences.stageTeam') }}</dt>
                <dd>{{ occurrence.team_name }}</dd>
              </div>
              <div>
                <dt class="text-xs text-muted-foreground">{{ $t('occurrences.priorityLabel') }}</dt>
                <dd>{{ $t(priorityLabels[occurrence.priority]) }}</dd>
              </div>
              <div v-if="occurrence.sla_resolution_deadline">
                <dt class="text-xs text-muted-foreground">{{ $t('occurrences.slaDeadline') }}</dt>
                <dd :class="occurrence.sla_breached ? 'font-medium text-destructive' : ''">{{ formatDateTime(occurrence.sla_resolution_deadline) }}</dd>
              </div>
              <div>
                <dt class="text-xs text-muted-foreground">{{ $t('occurrences.assigneeLabel') }}</dt>
                <dd>
                  <Select
                    :model-value="assigneeSelectValue"
                    :disabled="isUpdatingAssignee || (!!occurrence.can && !occurrence.can.reassign && !occurrence.can.claim)"
                    @update:model-value="handleAssigneeChange($event as string)"
                  >
                    <SelectTrigger class="-ml-2.5 h-7 w-auto min-w-40 border-none px-2.5 shadow-none">
                      <SelectValue :placeholder="$t('occurrences.unassigned')" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem :value="UNASSIGNED">{{ $t('occurrences.unassigned') }}</SelectItem>
                      <SelectItem v-for="u in activeUsers" :key="u.id" :value="u.id">{{ u.full_name }}</SelectItem>
                      <!-- Roles without users:read have no user list: still show who owns the case. -->
                      <SelectItem v-if="occurrence.assigned_user_id && !activeUsers.some(u => u.id === occurrence!.assigned_user_id)" :value="occurrence.assigned_user_id">
                        {{ occurrence.assigned_user_name }}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </dd>
              </div>
            </dl>
          </div>
        </section>

        <!-- 2 + 3. The request, and the order it refers to -->
        <div class="grid items-start gap-6 lg:grid-cols-3">
          <section class="rounded-lg border bg-card p-4 lg:col-span-2" data-testid="case-request">
            <h2 class="mb-3 text-sm font-semibold">{{ $t('occurrences.secRequest') }}</h2>
            <dl class="grid grid-cols-2 gap-x-6 gap-y-3 text-sm md:grid-cols-4">
              <div>
                <dt class="text-xs text-muted-foreground">{{ $t('occurrences.contactLabel') }}</dt>
                <dd>{{ occurrence.contact_name }}</dd>
              </div>
              <div v-if="occurrence.category_name">
                <dt class="text-xs text-muted-foreground">{{ $t('occurrences.categoryLabel') }}</dt>
                <dd>{{ occurrence.category_name }}</dd>
              </div>
              <div v-if="occurrence.sale_channel">
                <dt class="text-xs text-muted-foreground">{{ $t('occurrences.secOrigin') }}</dt>
                <dd>{{ occurrence.sale_channel }}</dd>
              </div>
              <div>
                <dt class="text-xs text-muted-foreground">{{ $t('occurrences.openedAtLabel') }}</dt>
                <dd>{{ formatDateTime(occurrence.opened_at) }}</dd>
              </div>
              <div v-if="occurrence.process_name">
                <dt class="text-xs text-muted-foreground">{{ $t('occurrences.secProcess') }}</dt>
                <dd>{{ occurrence.process_name }}</dd>
              </div>
              <div v-if="occurrence.what_happened_name">
                <dt class="text-xs text-muted-foreground">{{ $t('occurrences.whatHappenedLabel') }}</dt>
                <dd>{{ occurrence.what_happened_name }}</dd>
              </div>
              <div v-if="occurrence.updated_at">
                <dt class="text-xs text-muted-foreground">{{ $t('occurrences.updatedAt') }}</dt>
                <dd>{{ formatDateTime(occurrence.updated_at) }}</dd>
              </div>
            </dl>
            <div v-if="occurrence.description" class="mt-4 border-t pt-3">
              <p class="whitespace-pre-wrap text-sm leading-relaxed [overflow-wrap:anywhere]">{{ occurrence.description }}</p>
            </div>
          </section>

          <div class="space-y-6">
          <section class="rounded-lg border bg-card p-4" data-testid="case-order">
            <h2 class="mb-3 text-sm font-semibold">{{ $t('occurrences.secOrder') }}</h2>
            <template v-if="occurrence.order_ref || occurrence.invoice_number || occurrence.product_description">
              <dl class="space-y-2 text-sm">
                <div v-if="occurrence.order_ref">
                  <dt class="text-xs text-muted-foreground">{{ $t('occurrences.orderRef') }}</dt>
                  <dd class="font-mono">{{ occurrence.order_ref }}</dd>
                </div>
                <div v-if="occurrence.invoice_number">
                  <dt class="text-xs text-muted-foreground">{{ $t('occurrences.invoiceNumberLabel') }}</dt>
                  <dd class="font-mono">{{ occurrence.invoice_number }}</dd>
                </div>
                <div v-if="occurrence.purchase_date">
                  <dt class="text-xs text-muted-foreground">{{ $t('occurrences.purchaseDateLabel') }}</dt>
                  <dd>{{ formatDateTime(occurrence.purchase_date) }}</dd>
                </div>
                <div v-if="occurrence.product_description">
                  <dt class="text-xs text-muted-foreground">{{ $t('occurrences.secProduct') }}</dt>
                  <dd class="[overflow-wrap:anywhere]">{{ occurrence.product_description }}</dd>
                </div>
              </dl>
              <p v-if="occurrence.order_ref" class="mt-3 text-xs text-muted-foreground">{{ $t('occurrences.orderNoIntegration') }}</p>
            </template>
            <p v-else class="text-sm text-muted-foreground">{{ $t('occurrences.noOrder') }}</p>
          </section>
          <OccurrenceDocumentsCard :occurrence-id="occurrence.id" :can-write="canWrite" />
          </div>
        </div>

        <!-- 4. The customer (WhatsApp). Never the same place as the internal discussion. -->
        <!-- Customer and colleagues side by side: two clearly separate conversations, each at readable width. -->
        <div class="grid items-start gap-6 lg:grid-cols-2">
        <section id="case-customer" class="scroll-mt-4 space-y-3">
          <OccurrenceWhatsAppCard :occurrence-id="occurrence.id" @configured="waConfigured = $event" />
          <OccurrenceCommunicationCard :occurrence-id="occurrence.id" />
        <!-- Legacy reply (from the contact's own account): only when the case has no channel account -->
        <Card v-if="canWrite && (occurrence.can?.reply ?? true) && !waConfigured" class="border-primary/40">
          <CardHeader class="pb-3">
            <CardTitle class="text-sm font-medium">{{ $t('occurrences.replyTitle') }}</CardTitle>
            <p class="text-xs text-muted-foreground">{{ $t('occurrences.replyHint') }}</p>
          </CardHeader>
          <CardContent class="space-y-2">
            <div v-if="occurrence.process_id" class="flex items-center gap-2">
              <Select v-model="messageStage">
                <SelectTrigger class="w-56">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="documents">{{ $t('occurrences.stageDocuments') }}</SelectItem>
                  <SelectItem value="follow_up">{{ $t('occurrences.stageFollowUp') }}</SelectItem>
                  <SelectItem value="forwarding">{{ $t('occurrences.stageForwarding') }}</SelectItem>
                  <SelectItem value="closing">{{ $t('occurrences.stageClosing') }}</SelectItem>
                </SelectContent>
              </Select>
              <Button size="sm" variant="outline" :disabled="loadingSuggestion" @click="loadSuggestedMessage">
                <Loader2 v-if="loadingSuggestion" class="h-4 w-4 mr-2 animate-spin" />
                {{ $t('occurrences.loadSuggestedMessage') }}
              </Button>
            </div>
            <Textarea
              v-model="replyContent"
              :placeholder="$t('occurrences.replyPlaceholder')"
              :rows="4"
              @update:model-value="handleReplyInput($event as string)"
            />
            <p v-if="replyWindowClosed" class="text-sm text-destructive" role="alert">
              {{ $t('chat.sendProtocolWindowClosed') }}
            </p>
            <Button size="sm" :disabled="!replyContent.trim() || isReplying" @click="submitReply">
              <Loader2 v-if="isReplying" class="h-4 w-4 mr-2 animate-spin" />
              <Send v-else class="h-4 w-4 mr-2" />
              {{ $t('occurrences.sendReply') }}
            </Button>
          </CardContent>
        </Card>
        </section>

        <!-- 5. Colleagues only -->
        <section id="case-internal" class="scroll-mt-4 space-y-3">
          <OccurrenceChatPanel :occurrence-id="occurrence.id" />
        </section>
        </div>

        <!-- 6. Secondary: history and participants -->
        <div class="grid items-start gap-6 lg:grid-cols-3">
          <section id="case-history" class="scroll-mt-4 rounded-lg border bg-card p-4 lg:col-span-2" data-testid="case-history">
            <h2 class="mb-3 text-sm font-semibold">{{ $t('chat.occurrenceTimeline') }}</h2>
            <p v-if="store.events.length === 0" class="py-4 text-center text-sm text-muted-foreground">{{ $t('occurrences.noEventsYet') }}</p>
            <ol v-else class="space-y-3">
              <li v-for="event in shownEvents" :key="event.id" class="flex gap-3 text-sm">
                <component :is="eventIcons[event.type] || StickyNote" class="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                <div class="min-w-0">
                  <p class="flex flex-wrap items-baseline gap-x-2">
                    <span class="font-medium">{{ $t(eventLabels[event.type] || 'occurrences.eventUnknown') }}</span>
                    <span v-if="event.created_by_name" class="text-xs text-muted-foreground">{{ event.created_by_name }}</span>
                    <span class="text-xs text-muted-foreground">{{ formatDateTime(event.created_at) }}</span>
                  </p>
                  <p v-if="event.type === 'customer_notification'" class="mt-0.5 text-muted-foreground">{{ $t('occurrences.notice.' + event.content) }}</p>
                  <p v-else-if="event.content" class="mt-0.5 whitespace-pre-wrap text-muted-foreground [overflow-wrap:anywhere]">{{ event.content }}</p>
                </div>
              </li>
            </ol>
            <Button v-if="store.events.length > HISTORY_PREVIEW" variant="ghost" size="sm" class="mt-3" data-testid="history-toggle" :aria-expanded="showAllEvents" @click="showAllEvents = !showAllEvents">
              <ChevronDown class="mr-1 h-4 w-4 transition-transform" :class="showAllEvents ? 'rotate-180' : ''" aria-hidden="true" />
              {{ showAllEvents ? $t('occurrences.showLess') : $t('occurrences.showAll', { n: store.events.length }) }}
            </Button>
          </section>

          <div id="case-people" class="scroll-mt-4">
            <OccurrenceParticipantsCard :occurrence-id="occurrence.id" :can-write="canWrite && (occurrence.can?.add_participant ?? true)" />
          </div>
        </div>
      </div>
      </template>
    </DetailPageLayout>

    <DeleteConfirmDialog
      v-model:open="isDeleteDialogOpen"
      :title="$t('occurrences.deleteProtocolTitle')"
      :item-name="occurrence?.protocol_number"
      :is-submitting="isDeleting"
      @confirm="confirmDelete"
    >
      <p class="text-sm text-muted-foreground">{{ $t('occurrences.deleteProtocolWarning') }}</p>
    </DeleteConfirmDialog>
  </div>
</template>
