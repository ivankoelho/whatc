<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import DetailPageLayout from '@/components/shared/DetailPageLayout.vue'
import { IconButton, DeleteConfirmDialog } from '@/components/shared'
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

const occurrenceId = computed(() => route.params.id as string)
const occurrence = ref<Occurrence | null>(null)
const isLoading = ref(true)
const isNotFound = ref(false)
const isSendingProtocol = ref(false)
const isAddingNote = ref(false)
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
const newNoteContent = ref('')
const isUpdatingAssignee = ref(false)
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
}

const breadcrumbs = computed(() => [
  { label: t('occurrences.title'), href: '/crm/occurrences' },
  { label: occurrence.value?.protocol_number || '' },
])

const activeUsers = computed(() => usersStore.users.filter(u => u.is_active))
const assigneeSelectValue = computed(() => occurrence.value?.assigned_user_id || UNASSIGNED)

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

async function submitNote() {
  if (!occurrence.value || !newNoteContent.value.trim()) return
  isAddingNote.value = true
  try {
    await store.addNote(occurrence.value.id, newNoteContent.value.trim())
    newNoteContent.value = ''
    toast.success(t('occurrences.noteAdded'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.noteAddFailed')))
  } finally {
    isAddingNote.value = false
  }
}

onMounted(async () => {
  await Promise.all([
    store.fetchStages(),
    loadOccurrence(),
    store.fetchEvents(occurrenceId.value),
    usersStore.users.length === 0 ? usersStore.fetchUsers().catch(() => {}) : Promise.resolve(),
  ])

  unsubscribeOccurrenceChanged = wsService.onOccurrenceChanged(handleOccurrenceChanged)
  unsubscribeOccurrenceEventCreated = wsService.onOccurrenceEventCreated(handleOccurrenceEventCreated)
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
      :is-loading="isLoading"
      :is-not-found="isNotFound"
      :not-found-title="$t('occurrences.notFound')"
    >
      <template #actions>
        <div v-if="occurrence" class="flex items-center gap-2">
          <Select :model-value="occurrence.stage_id" @update:model-value="handleStageChange($event as string)">
            <SelectTrigger class="w-48">
              <SelectValue :placeholder="$t('occurrences.selectStage')" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="stage in store.stages" :key="stage.id" :value="stage.id">
                {{ stage.name }}
              </SelectItem>
            </SelectContent>
          </Select>
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
        <!-- Details -->
        <Card>
          <CardHeader class="pb-3">
            <div class="flex items-center justify-between gap-2">
              <div class="flex items-center gap-1.5">
                <CardTitle class="text-lg font-semibold font-mono tracking-wide">{{ occurrence.protocol_number }}</CardTitle>
                <IconButton
                  :icon="Copy"
                  :label="$t('occurrences.copyProtocol')"
                  class="h-7 w-7"
                  @click="copyProtocol"
                />
              </div>
              <Badge variant="outline" :style="{ borderColor: store.stageColor(occurrence.stage_id), color: store.stageColor(occurrence.stage_id) }">
                {{ occurrence.stage_name }}
              </Badge>
            </div>
          </CardHeader>
          <CardContent class="space-y-3 text-sm">
            <div>
              <span class="text-muted-foreground text-xs">{{ $t('occurrences.contactLabel') }}</span>
              <p>{{ occurrence.contact_name }}</p>
            </div>
            <div v-if="occurrence.description">
              <span class="text-muted-foreground text-xs">{{ $t('chat.occurrenceDescription') }}</span>
              <p class="whitespace-pre-wrap">{{ occurrence.description }}</p>
            </div>
            <div class="grid grid-cols-3 gap-3">
              <div>
                <span class="text-muted-foreground text-xs">{{ $t('occurrences.priorityLabel') }}</span>
                <p>{{ $t(priorityLabels[occurrence.priority]) }}</p>
              </div>
              <div>
                <span class="text-muted-foreground text-xs">{{ $t('occurrences.assigneeLabel') }}</span>
                <Select
                  :model-value="assigneeSelectValue"
                  :disabled="isUpdatingAssignee"
                  @update:model-value="handleAssigneeChange($event as string)"
                >
                  <SelectTrigger class="h-8 mt-1 -ml-2.5 border-none shadow-none px-2.5">
                    <SelectValue :placeholder="$t('occurrences.unassigned')" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem :value="UNASSIGNED">{{ $t('occurrences.unassigned') }}</SelectItem>
                    <SelectItem v-for="u in activeUsers" :key="u.id" :value="u.id">
                      {{ u.full_name }}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div>
                <span class="text-muted-foreground text-xs">{{ $t('occurrences.openedAtLabel') }}</span>
                <p>{{ formatDateTime(occurrence.opened_at) }}</p>
              </div>
            </div>
          </CardContent>
        </Card>

        <!-- Timeline -->
        <Card>
          <CardHeader class="pb-3">
            <CardTitle class="text-sm font-medium">{{ $t('chat.occurrenceTimeline') }}</CardTitle>
          </CardHeader>
          <CardContent>
            <p v-if="store.events.length === 0" class="text-sm text-muted-foreground text-center py-6">
              {{ $t('occurrences.noEventsYet') }}
            </p>
            <div v-else class="relative">
              <div class="absolute left-3 top-3 bottom-3 w-px bg-border" />
              <div v-for="event in store.events" :key="event.id" class="relative pl-9 pb-6 last:pb-0">
                <div class="absolute left-1.5 top-1 w-3 h-3 rounded-full border-2 border-background bg-muted-foreground flex items-center justify-center" />
                <div class="flex items-center gap-2 flex-wrap">
                  <component :is="eventIcons[event.type] || StickyNote" class="h-3.5 w-3.5 text-muted-foreground" />
                  <span class="text-sm font-medium">{{ $t(eventLabels[event.type] || 'occurrences.eventUnknown') }}</span>
                  <span v-if="event.created_by_name" class="text-xs text-muted-foreground">{{ event.created_by_name }}</span>
                  <span class="text-xs text-muted-foreground">{{ formatDateTime(event.created_at) }}</span>
                </div>
                <p v-if="event.content" class="text-sm mt-1 whitespace-pre-wrap">{{ event.content }}</p>
              </div>
            </div>

            <!-- Add note (internal) -->
            <div class="mt-6 space-y-2">
              <div>
                <p class="text-sm font-medium">{{ $t('occurrences.internalNoteTitle') }}</p>
                <p class="text-xs text-muted-foreground">{{ $t('occurrences.internalNoteHint') }}</p>
              </div>
              <Textarea
                v-model="newNoteContent"
                :placeholder="$t('occurrences.writeNote')"
                :rows="3"
              />
              <Button size="sm" :disabled="!newNoteContent.trim() || isAddingNote" @click="submitNote">
                <Loader2 v-if="isAddingNote" class="h-4 w-4 mr-2 animate-spin" />
                {{ $t('occurrences.addNote') }}
              </Button>
            </div>
          </CardContent>
        </Card>

        <!-- Reply to customer (sent via WhatsApp) -->
        <Card v-if="canWrite" class="border-primary/40">
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
