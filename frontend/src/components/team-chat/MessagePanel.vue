<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowDown, ArrowLeft, Check, CornerUpLeft, FileText, Hash, Loader2, Lock, MessageSquare, Pencil, Settings2, SmilePlus, Trash2, Users, X } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/shared'
import { Badge } from '@/components/ui/badge'
import ChannelManagePanel from './ChannelManagePanel.vue'
import { useInfiniteScroll } from '@/composables/useInfiniteScroll'
import { usePendingPoll } from '@/composables/usePendingPoll'
import { useTeamChatStore } from '@/stores/teamChat'
import { useAuthStore } from '@/stores/auth'
import { getAvatarGradient, getInitials } from '@/lib/utils'
import { getErrorMessage } from '@/lib/api-utils'
import MessageAttachments from './MessageAttachments.vue'
import PollCard from './PollCard.vue'
import MessageComposer from './MessageComposer.vue'
import { bubbleClass, bubbleMeta, dayLabel, isOwnMessage } from '@/lib/chat-bubbles'
import { TEAM_CHAT_REACTIONS, chatErrorCode, isSendableBody, reactionGlyph, systemEventKey } from '@/lib/team-chat'
import type { TeamChatMessage, TeamChatReplyTo } from '@/services/api'

// The conversation: header, the list of messages (reactions, quotes, actions, edit and delete) and
// scrolling. Writing is MessageComposer's job; this panel only follows what was sent, hands it files
// dropped anywhere on the conversation, and focuses it when a reply starts.
const props = defineProps<{ channelId: string; showBack?: boolean }>()
const emit = defineEmits<{ back: [] }>()

const { t, locale } = useI18n()
const store = useTeamChatStore()
const auth = useAuthStore()

const editingId = ref<string | null>(null)
const editingBody = ref('')
const endRef = ref<HTMLElement | null>(null)

const panel = computed(() => store.panel(props.channelId))
// An attachment still being checked for malware: refresh by itself (controlled polling) until released.
const hasUnsettledFile = computed(() => panel.value.messages.some(m => m.attachments?.some(a => a.scan_status === 'pending' || a.scan_status === 'error')))
usePendingPoll(hasUnsettledFile, () => store.refreshLatest(props.channelId))
const channel = computed(() => store.channelById(props.channelId))
const isArchived = computed(() => !!channel.value?.archived_at)
const manageOpen = ref(false)

// A system message is the channel's own record ("Ana added Bia", "Carlos entered as
// administrator: <reason>"). It is translated here from its event and parameters and
// shown as text, never as HTML.
function systemText(m: TeamChatMessage): string {
  const key = systemEventKey(m.meta)
  if (!key) return ''
  const meta = m.meta ?? {}
  return t(key, {
    actor: meta.actor_name ?? '', target: meta.target_name ?? '',
    old: meta.old_name ?? '', new: meta.new_name ?? '', reason: meta.reason ?? ''
  })
}
const myId = computed(() => auth.user?.id)

// Buttons mirror what the server allows; the server still decides.
const canWrite = computed(() => auth.hasPermission('team_chat', 'write'))
const canModerate = computed(() => auth.hasPermission('team_chat', 'delete'))
// Reacting and replying are writes: the same people and the same unarchived channels as sending.
const canInteract = computed(() => canWrite.value && !isArchived.value)

const composer = ref<InstanceType<typeof MessageComposer> | null>(null)

// ---- Drop files anywhere on the conversation: the composer decides what to do with them ----
const dragging = ref(false)

function onDragOver(e: DragEvent) {
  if (canInteract.value && e.dataTransfer?.types.includes('Files')) dragging.value = true
}

function onDrop(e: DragEvent) {
  dragging.value = false
  composer.value?.attach(e.dataTransfer?.files)
}

const attachmentLabel = (kind?: string, name?: string) =>
  kind ? t(`teamChat.attachments.quote.${kind}`, { name: name ?? '' }) : ''
const quoteText = (r: TeamChatReplyTo) => r.snippet || attachmentLabel(r.attachment_kind, r.attachment_name)
const pickerFor = ref<string | null>(null)
const highlightId = ref<string | null>(null)

function startReply(m: TeamChatMessage) {
  store.startReply(m)
  pickerFor.value = null
  void nextTick(() => composer.value?.focus())
}

async function toggleReaction(m: TeamChatMessage, code: string) {
  pickerFor.value = null
  const mine = !!m.reactions?.find(r => r.code === code)?.mine
  try {
    await store.react(m, code, !mine)
  } catch (err) {
    const c = chatErrorCode(err)
    toast.error(t(['archived', 'forbidden', 'too_many_reactions', 'not_found'].includes(c) ? `teamChat.errors.${c}` : 'teamChat.errors.generic'))
  }
}

// Scrolls to the quoted message when it is already loaded; otherwise does nothing
// (loading history up to it is not worth a request storm).
function goToMessage(id: string) {
  const el = document.querySelector(`[data-testid="message-${id}"]`)
  if (!el) return
  el.scrollIntoView({ behavior: 'smooth', block: 'center' })
  highlightId.value = id
  setTimeout(() => { if (highlightId.value === id) highlightId.value = null }, 1500)
}

const reactionTitle = (r: { names: string[]; count: number }) =>
  r.names.join(', ') + (r.count > r.names.length ? ` +${r.count - r.names.length}` : '')

// Reading old history must not be yanked to the bottom by a new message: follow only when already
// near the end (or when the message is mine); otherwise count what arrived and offer a jump.
const atBottom = ref(true)
const unseen = ref(0)
const NEAR_BOTTOM_PX = 80
function trackBottom(e: Event) {
  const el = e.target as HTMLElement
  atBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX
  if (atBottom.value) unseen.value = 0
}

const scroll = useInfiniteScroll({
  direction: 'top',
  onScroll: trackBottom,
  onLoadMore: async () => {
    await scroll.preserveScrollPosition(async () => {
      await store.loadOlder(props.channelId)
      await nextTick()
    })
  },
  hasMore: computed(() => panel.value.hasMore),
  isLoading: computed(() => panel.value.loadingOlder)
})

function scrollToBottom(instant = false) {
  unseen.value = 0
  atBottom.value = true
  void nextTick(() => endRef.value?.scrollIntoView({ behavior: instant ? 'instant' : 'smooth', block: 'end' }))
}

function arm() {
  scroll.cleanup()
  unseen.value = 0
  void nextTick(() => setTimeout(() => {
    scrollToBottom(true)
    scroll.setup()
  }, 50))
}

// The reaction picker closes on a click anywhere else, or Escape, like any popover
// (the composer's emoji panel does the same for itself).
function onDocPointerDown(e: PointerEvent) {
  const target = e.target as Element | null
  if (pickerFor.value && !target?.closest?.('[data-testid^="actions-"]')) pickerFor.value = null
}
function onDocKeyDown(e: KeyboardEvent) {
  if (e.key === 'Escape') pickerFor.value = null
}
onMounted(() => {
  arm()
  document.addEventListener('pointerdown', onDocPointerDown)
  document.addEventListener('keydown', onDocKeyDown)
})
onBeforeUnmount(() => {
  scroll.cleanup()
  document.removeEventListener('pointerdown', onDocPointerDown)
  document.removeEventListener('keydown', onDocKeyDown)
})
watch(() => props.channelId, () => {
  editingId.value = null
  pickerFor.value = null
  arm()
})
// Re-arm once the first page arrives (the list was empty while loading).
watch(() => panel.value.loading, (now, was) => { if (was && !now) arm() })
// New message at the bottom: follow it.
watch(() => panel.value.messages[panel.value.messages.length - 1]?.id, (now, was) => {
  if (!was || !now || now === was) return
  const last = panel.value.messages[panel.value.messages.length - 1]
  if (atBottom.value || isOwnMessage(last, myId.value)) scrollToBottom()
  else unseen.value++
})

function formatTime(iso: string) {
  return new Date(iso).toLocaleTimeString(locale.value, { hour: '2-digit', minute: '2-digit' })
}

const isOwn = (m: TeamChatMessage) => isOwnMessage(m, myId.value)
const metaOf = (i: number) => bubbleMeta(panel.value.messages, i)
const dayText = (iso: string) => dayLabel(iso, locale.value, { today: t('teamChat.today'), yesterday: t('teamChat.yesterday') })
const canEdit = (m: TeamChatMessage) => isOwn(m) && canWrite.value
const canDelete = (m: TeamChatMessage) => isOwn(m) || canModerate.value

function startEdit(m: TeamChatMessage) {
  editingId.value = m.id
  editingBody.value = m.body
}

async function saveEdit(m: TeamChatMessage) {
  if (!isSendableBody(editingBody.value)) return
  try {
    await store.edit(m, editingBody.value.trim())
    editingId.value = null
  } catch (err) {
    toast.error(getErrorMessage(err, t('teamChat.editFailed')))
  }
}

async function removeMessage(m: TeamChatMessage) {
  if (!window.confirm(t('teamChat.confirmDelete'))) return
  try {
    await store.remove(m)
  } catch (err) {
    toast.error(getErrorMessage(err, t('teamChat.deleteFailed')))
  }
}
</script>

<template>
  <section
    class="relative flex h-full min-w-0 flex-1 flex-col bg-background" :aria-label="channel?.name || t('teamChat.title')"
    @dragenter.prevent="onDragOver" @dragover.prevent="onDragOver"
  >
    <div
      v-if="dragging" class="absolute inset-0 z-30 flex items-center justify-center border-2 border-dashed border-primary bg-background/90 text-sm font-medium"
      data-testid="drop-overlay" @dragover.prevent @dragleave.prevent="dragging = false" @drop.prevent="onDrop"
    >{{ t('teamChat.attachments.drop') }}</div>
    <header class="flex items-center gap-2 border-b px-4 py-3">
      <Button v-if="showBack" variant="ghost" size="icon" class="h-10 w-10 md:hidden" :aria-label="t('common.back')" @click="emit('back')">
        <ArrowLeft class="h-4 w-4" />
      </Button>
      <Users v-if="channel?.kind === 'team'" class="h-4 w-4 text-muted-foreground" />
      <Hash v-else-if="channel?.kind === 'channel'" class="h-4 w-4 text-muted-foreground" />
      <FileText v-else-if="channel?.kind === 'occurrence'" class="h-4 w-4 text-muted-foreground" />
      <Lock v-else class="h-4 w-4 text-muted-foreground" />
      <h2 class="truncate text-sm font-semibold">{{ channel?.name || t('teamChat.unnamed') }}</h2>
      <Badge v-if="isArchived" variant="secondary">{{ t('teamChat.manage.archivedBadge') }}</Badge>
      <Button v-if="channel?.kind === 'occurrence' && channel.occurrence_id" as-child variant="outline" size="sm" class="ml-auto" data-testid="open-occurrence">
        <RouterLink :to="`/crm/occurrences/${channel.occurrence_id}`"><FileText class="mr-1.5 h-4 w-4" aria-hidden="true" />{{ t('teamChat.openOccurrence') }}</RouterLink>
      </Button>
      <Button
        v-if="channel?.kind === 'channel'"
        variant="ghost" size="icon" class="ml-auto h-9 w-9"
        :aria-label="t('teamChat.manage.title')" :title="t('teamChat.manage.title')"
        @click="manageOpen = true"
      >
        <Settings2 class="h-4 w-4" />
      </Button>
    </header>

    <!-- No access (or it does not exist: the server answers the same) -->
    <EmptyState
      v-if="panel.error === 'forbidden'"
      class="flex-1"
      :icon="Lock"
      :title="t('teamChat.noAccessTitle')"
      :description="t('teamChat.noAccess')"
    />

    <ErrorState
      v-else-if="panel.error === 'failed'"
      class="flex-1"
      :title="t('teamChat.messagesFailedTitle')"
      :description="t('teamChat.messagesFailed')"
      :retry-label="t('common.retry')"
      @retry="store.loadMessages(channelId)"
    />

    <template v-else>
      <!-- the viewport wrapper is min-width: fit-content, which lets a long line stretch the whole list past the pane: undo that so text wraps -->
      <div class="relative flex min-h-0 flex-1 flex-col">
      <ScrollArea :ref="(el: any) => scroll.scrollAreaRef.value = el" class="flex-1 [&_[data-reka-scroll-area-viewport]>div]:!min-w-0">
        <div class="space-y-1 p-4" aria-live="polite">
          <div v-if="panel.loadingOlder" class="flex justify-center py-2">
            <Loader2 class="h-4 w-4 animate-spin text-muted-foreground" />
          </div>

          <div v-if="panel.loading && panel.messages.length === 0" class="space-y-3" data-testid="messages-loading">
            <Skeleton v-for="i in 4" :key="i" class="h-12 w-3/4" />
          </div>

          <EmptyState
            v-else-if="panel.messages.length === 0"
            :icon="MessageSquare"
            :title="t('teamChat.noMessagesTitle')"
            :description="t('teamChat.noMessages')"
          />

          <template v-for="(m, i) in panel.messages" :key="m.id">
          <div v-if="metaOf(i).newDay" class="flex justify-center py-2" :data-testid="`day-${m.id}`">
            <span class="rounded-full bg-muted px-3 py-1 text-xs text-muted-foreground">{{ dayText(m.created_at) }}</span>
          </div>
          <div v-if="m.kind === 'system'" :data-testid="`system-${m.id}`" class="flex justify-center px-2 py-1">
            <p class="max-w-[90%] break-words rounded-full bg-muted px-3 py-1 text-center text-xs text-muted-foreground">{{ systemText(m) }}</p>
          </div>
          <article
            v-else
            :data-testid="`message-${m.id}`"
            :data-own="isOwn(m)"
            :class="['group relative flex flex-wrap items-end gap-x-2 rounded-md px-2 transition-colors md:flex-nowrap', isOwn(m) ? 'flex-row-reverse justify-start' : 'justify-start', metaOf(i).first ? 'mt-2' : 'mt-0.5', highlightId === m.id ? 'bg-primary/10' : '']"
          >
            <!-- The avatar sits beside the first bubble of a run by someone else; own messages have none. -->
            <div v-if="!isOwn(m)" class="h-8 w-8 shrink-0">
              <Avatar v-if="metaOf(i).first" class="h-8 w-8">
                <AvatarFallback :class="'bg-gradient-to-br text-[11px] text-white ' + getAvatarGradient(m.user_name)">
                  {{ getInitials(m.user_name || '?') }}
                </AvatarFallback>
              </Avatar>
            </div>
            <div :class="['min-w-0 max-w-[88%] rounded-2xl px-3 py-1.5 shadow-sm md:max-w-[min(36rem,70%)]', bubbleClass(isOwn(m)), metaOf(i).first ? '' : (isOwn(m) ? 'rounded-tr-2xl' : 'rounded-tl-2xl'), editingId === m.id ? 'flex-1' : '']">
              <span v-if="!isOwn(m) && metaOf(i).first" class="block text-xs font-semibold text-primary">{{ m.user_name }}</span>

              <button
                v-if="m.reply_to" type="button" :data-testid="`quote-${m.id}`"
                class="mt-0.5 block w-full max-w-full truncate border-l-2 border-primary/50 bg-muted/60 px-2 py-0.5 text-left text-xs text-muted-foreground"
                @click="goToMessage(m.reply_to.id)"
              >
                <template v-if="m.reply_to.deleted"><em>{{ t('teamChat.removedMessage') }}</em></template>
                <template v-else><strong class="font-medium">{{ m.reply_to.user_name }}</strong> {{ quoteText(m.reply_to) }}</template>
              </button>

              <div v-if="editingId === m.id" class="mt-1 space-y-2">
                <Textarea
                  v-model="editingBody"
                  class="min-h-[60px] resize-none text-sm"
                  :aria-label="t('teamChat.editMessage')"
                  @keydown.enter.exact.prevent="saveEdit(m)"
                  @keydown.esc="editingId = null"
                />
                <div class="flex justify-end gap-1.5">
                  <Button variant="ghost" size="sm" class="h-7 text-xs" @click="editingId = null">
                    <X class="mr-1 h-3 w-3" />{{ t('common.cancel') }}
                  </Button>
                  <Button size="sm" class="h-7 text-xs" :disabled="!isSendableBody(editingBody)" @click="saveEdit(m)">
                    <Check class="mr-1 h-3 w-3" />{{ t('common.save') }}
                  </Button>
                </div>
              </div>
              <!-- Plain text, escaped by Vue; never v-html -->
              <PollCard v-else-if="m.poll" :message="m" :can-vote="canWrite && !isArchived" />
              <p v-else-if="m.body" class="whitespace-pre-wrap text-sm [overflow-wrap:anywhere]">{{ m.body }}</p>

              <MessageAttachments v-if="m.attachments?.length" :attachments="m.attachments" />

              <div v-if="m.reactions?.length" class="mt-1 flex flex-wrap gap-1" :data-testid="`reactions-${m.id}`">
                <component
                  :is="canInteract ? 'button' : 'span'"
                  v-for="r in m.reactions" :key="r.code"
                  :type="canInteract ? 'button' : undefined"
                  :data-testid="`reaction-${m.id}-${r.code}`"
                  :aria-pressed="canInteract ? r.mine : undefined"
                  :aria-label="t(`teamChat.reactions.${r.code}`) + ' ' + r.count"
                  :title="reactionTitle(r)"
                  :class="['inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs', r.mine ? 'border-primary bg-primary/10' : 'bg-background', canInteract ? 'hover:bg-muted' : '']"
                  @click="canInteract && toggleReaction(m, r.code)"
                >{{ reactionGlyph(r.code) }} {{ r.count }}</component>
              </div>

              <div class="mt-0.5 flex items-center justify-end gap-1.5 text-[11px] text-muted-foreground">
                <span v-if="m.edited_at">{{ t('teamChat.edited') }}</span>
                <time :datetime="m.created_at">{{ formatTime(m.created_at) }}</time>
              </div>
            </div>

            <!-- Right after the message (not at the far edge of the pane), so the actions stay near what they act on -->
            <div
              v-if="editingId !== m.id && (canInteract || canEdit(m) || canDelete(m))"
              class="relative flex shrink-0 gap-0.5 transition-opacity max-md:basis-full max-md:justify-end md:opacity-0 md:focus-within:opacity-100 md:group-hover:opacity-100"
              :class="pickerFor === m.id ? 'md:opacity-100' : ''" :data-testid="`actions-${m.id}`"
            >
              <button v-if="canInteract" type="button" class="flex h-9 w-9 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground md:h-7 md:w-7" :aria-label="t('teamChat.react')" @click="pickerFor = pickerFor === m.id ? null : m.id">
                <SmilePlus class="h-3.5 w-3.5" />
              </button>
              <button v-if="canInteract" type="button" class="flex h-9 w-9 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground md:h-7 md:w-7" :aria-label="t('teamChat.reply')" @click="startReply(m)">
                <CornerUpLeft class="h-3.5 w-3.5" />
              </button>
              <button v-if="canEdit(m)" type="button" class="flex h-9 w-9 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground md:h-7 md:w-7" :aria-label="t('teamChat.editMessage')" @click="startEdit(m)">
                <Pencil class="h-3.5 w-3.5" />
              </button>
              <button v-if="canDelete(m)" type="button" class="flex h-9 w-9 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-destructive md:h-7 md:w-7" :aria-label="t('teamChat.deleteMessage')" @click="removeMessage(m)">
                <Trash2 class="h-3.5 w-3.5" />
              </button>
              <div
                v-if="pickerFor === m.id" role="menu" :data-testid="`picker-${m.id}`"
                class="absolute left-0 top-full z-20 mt-1 grid w-max md:left-auto md:right-0 max-w-[calc(100vw-1rem)] grid-cols-6 gap-0.5 rounded-md border bg-popover p-1 shadow-md"
              >
                <button
                  v-for="r in TEAM_CHAT_REACTIONS" :key="r.code" type="button" role="menuitem"
                  class="h-9 w-9 rounded text-lg hover:bg-muted md:h-8 md:w-8" :aria-label="t(`teamChat.reactions.${r.code}`)"
                  :data-testid="`pick-${m.id}-${r.code}`" @click="toggleReaction(m, r.code)"
                >{{ r.glyph }}</button>
              </div>
            </div>
          </article>
          </template>
          <div ref="endRef" />
        </div>
      </ScrollArea>
      <button
        v-if="unseen > 0" type="button" data-testid="new-messages"
        class="absolute bottom-3 left-1/2 flex -translate-x-1/2 items-center gap-1.5 rounded-full bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground shadow-lg"
        @click="scrollToBottom()"
      >
        <ArrowDown class="h-3.5 w-3.5" aria-hidden="true" />{{ t('teamChat.newMessages', { n: unseen }, unseen) }}
      </button>
      </div>

      <MessageComposer
        ref="composer" :channel-id="channelId" :can-write="canWrite" :archived="isArchived"
        @sent="scrollToBottom()"
      />
    </template>

    <ChannelManagePanel
      v-if="channel?.kind === 'channel'"
      v-model:open="manageOpen"
      :channel-id="channelId"
      @left="emit('back')"
    />
  </section>
</template>
