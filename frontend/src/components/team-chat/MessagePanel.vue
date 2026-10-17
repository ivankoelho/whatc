<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowLeft, Check, CornerUpLeft, Hash, Loader2, Lock, MessageSquare, Pencil, Send, Settings2, SmilePlus, Trash2, Users, X } from 'lucide-vue-next'
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
import { useTeamChatStore } from '@/stores/teamChat'
import { useAuthStore } from '@/stores/auth'
import { getAvatarGradient, getInitials } from '@/lib/utils'
import { getErrorMessage } from '@/lib/api-utils'
import { TEAM_CHAT_MAX_BODY, TEAM_CHAT_REACTIONS, chatErrorCode, isSendableBody, pickTypingNames, reactionGlyph, systemEventKey } from '@/lib/team-chat'
import type { TeamChatMessage } from '@/services/api'

const props = defineProps<{ channelId: string; showBack?: boolean }>()
const emit = defineEmits<{ back: [] }>()

const { t, locale } = useI18n()
const store = useTeamChatStore()
const auth = useAuthStore()

const draft = ref('')
const sending = ref(false)
const editingId = ref<string | null>(null)
const editingBody = ref('')
const endRef = ref<HTMLElement | null>(null)

const panel = computed(() => store.panel(props.channelId))
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

const replyTo = computed(() => store.replyTarget(props.channelId))
const pickerFor = ref<string | null>(null)
const highlightId = ref<string | null>(null)

function startReply(m: TeamChatMessage) {
  store.startReply(m)
  pickerFor.value = null
  void nextTick(() => document.getElementById('team-chat-composer')?.focus())
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

const typingNames = computed(() => pickTypingNames(store.typingUserIds(props.channelId), store.userNames))
const typingText = computed(() => {
  const names = typingNames.value
  if (names.length === 0) return ''
  if (names.length === 1) return t('teamChat.typingOne', { name: names[0] })
  return t('teamChat.typingMany')
})
const overLimit = computed(() => Array.from(draft.value.trim()).length > TEAM_CHAT_MAX_BODY)

const scroll = useInfiniteScroll({
  direction: 'top',
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
  void nextTick(() => endRef.value?.scrollIntoView({ behavior: instant ? 'instant' : 'smooth', block: 'end' }))
}

function arm() {
  scroll.cleanup()
  void nextTick(() => setTimeout(() => {
    scrollToBottom(true)
    scroll.setup()
  }, 50))
}

onMounted(arm)
onBeforeUnmount(() => scroll.cleanup())
watch(() => props.channelId, () => {
  editingId.value = null
  pickerFor.value = null
  draft.value = ''
  arm()
})
// Re-arm once the first page arrives (the list was empty while loading).
watch(() => panel.value.loading, (now, was) => { if (was && !now) arm() })
// New message at the bottom: follow it.
watch(() => panel.value.messages[panel.value.messages.length - 1]?.id, (now, was) => {
  if (was && now && now !== was) scrollToBottom()
})

function formatTime(iso: string) {
  return new Date(iso).toLocaleTimeString(locale.value, { hour: '2-digit', minute: '2-digit' })
}

let inFlight = 0

async function submit() {
  if (!isSendableBody(draft.value)) return
  // Clear the box before the request: whatever is typed while it is in flight is
  // the next message and must survive. A failed send puts the text back.
  const body = draft.value.trim()
  draft.value = ''
  inFlight++
  sending.value = true
  try {
    await store.send(props.channelId, body)
    scrollToBottom()
  } catch (err) {
    if (!draft.value) draft.value = body
    if (chatErrorCode(err) === 'reply_target_gone') toast.error(t('teamChat.errors.reply_target_gone'))
    else toast.error(getErrorMessage(err, t('teamChat.sendFailed')))
  } finally {
    inFlight--
    sending.value = inFlight > 0
  }
}

function onComposerKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && replyTo.value) {
    store.cancelReply(props.channelId)
    return
  }
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    void submit()
  }
}

function onInput() {
  if (draft.value.trim()) store.notifyTyping(props.channelId)
}

const isOwn = (m: TeamChatMessage) => m.user_id === myId.value
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
  <section class="flex h-full min-w-0 flex-1 flex-col bg-background" :aria-label="channel?.name || t('teamChat.title')">
    <header class="flex items-center gap-2 border-b px-4 py-3">
      <Button v-if="showBack" variant="ghost" size="icon" class="h-10 w-10 md:hidden" :aria-label="t('common.back')" @click="emit('back')">
        <ArrowLeft class="h-4 w-4" />
      </Button>
      <Users v-if="channel?.kind === 'team'" class="h-4 w-4 text-muted-foreground" />
      <Hash v-else-if="channel?.kind === 'channel'" class="h-4 w-4 text-muted-foreground" />
      <Lock v-else class="h-4 w-4 text-muted-foreground" />
      <h2 class="truncate text-sm font-semibold">{{ channel?.name || t('teamChat.unnamed') }}</h2>
      <Badge v-if="isArchived" variant="secondary">{{ t('teamChat.manage.archivedBadge') }}</Badge>
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
      <ScrollArea :ref="(el: any) => scroll.scrollAreaRef.value = el" class="flex-1">
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

          <template v-for="m in panel.messages" :key="m.id">
          <div v-if="m.kind === 'system'" :data-testid="`system-${m.id}`" class="flex justify-center px-2 py-1">
            <p class="max-w-[90%] break-words rounded-full bg-muted px-3 py-1 text-center text-xs text-muted-foreground">{{ systemText(m) }}</p>
          </div>
          <article
            v-else
            :data-testid="`message-${m.id}`"
            :class="['group relative flex items-start gap-3 rounded-md px-2 py-1.5 transition-colors hover:bg-muted/50', highlightId === m.id ? 'bg-primary/10' : '']"
          >
            <Avatar class="mt-0.5 h-8 w-8 shrink-0">
              <AvatarFallback :class="'bg-gradient-to-br text-[11px] text-white ' + getAvatarGradient(m.user_name)">
                {{ getInitials(m.user_name || '?') }}
              </AvatarFallback>
            </Avatar>
            <div class="min-w-0 flex-1">
              <div class="flex items-baseline gap-2">
                <span class="text-sm font-semibold">{{ m.user_name }}</span>
                <time class="text-[11px] text-muted-foreground" :datetime="m.created_at">{{ formatTime(m.created_at) }}</time>
                <span v-if="m.edited_at" class="text-[11px] text-muted-foreground">{{ t('teamChat.edited') }}</span>
              </div>

              <button
                v-if="m.reply_to" type="button" :data-testid="`quote-${m.id}`"
                class="mt-0.5 block w-full max-w-full truncate border-l-2 border-primary/50 bg-muted/60 px-2 py-0.5 text-left text-xs text-muted-foreground"
                @click="goToMessage(m.reply_to.id)"
              >
                <template v-if="m.reply_to.deleted"><em>{{ t('teamChat.removedMessage') }}</em></template>
                <template v-else><strong class="font-medium">{{ m.reply_to.user_name }}</strong> {{ m.reply_to.snippet }}</template>
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
              <p v-else class="whitespace-pre-wrap break-words text-sm">{{ m.body }}</p>

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
            </div>

            <div v-if="editingId !== m.id && (canInteract || canEdit(m) || canDelete(m))" class="flex gap-0.5 transition-opacity md:opacity-0 md:focus-within:opacity-100 md:group-hover:opacity-100" :class="pickerFor === m.id ? 'md:opacity-100' : ''">
              <button v-if="canInteract" type="button" class="rounded p-2 text-muted-foreground md:p-1 hover:bg-muted hover:text-foreground" :aria-label="t('teamChat.react')" @click="pickerFor = pickerFor === m.id ? null : m.id">
                <SmilePlus class="h-3.5 w-3.5" />
              </button>
              <button v-if="canInteract" type="button" class="rounded p-2 text-muted-foreground md:p-1 hover:bg-muted hover:text-foreground" :aria-label="t('teamChat.reply')" @click="startReply(m)">
                <CornerUpLeft class="h-3.5 w-3.5" />
              </button>
              <button v-if="canEdit(m)" type="button" class="rounded p-2 text-muted-foreground md:p-1 hover:bg-muted hover:text-foreground" :aria-label="t('teamChat.editMessage')" @click="startEdit(m)">
                <Pencil class="h-3.5 w-3.5" />
              </button>
              <button v-if="canDelete(m)" type="button" class="rounded p-2 text-muted-foreground md:p-1 hover:bg-muted hover:text-destructive" :aria-label="t('teamChat.deleteMessage')" @click="removeMessage(m)">
                <Trash2 class="h-3.5 w-3.5" />
              </button>
            </div>
            <div
              v-if="pickerFor === m.id" role="menu" :data-testid="`picker-${m.id}`"
              class="absolute right-2 top-full z-20 -mt-1 grid grid-cols-6 gap-0.5 rounded-md border bg-popover p-1 shadow-md"
            >
              <button
                v-for="r in TEAM_CHAT_REACTIONS" :key="r.code" type="button" role="menuitem"
                class="h-9 w-9 rounded text-lg hover:bg-muted md:h-8 md:w-8" :aria-label="t(`teamChat.reactions.${r.code}`)"
                :data-testid="`pick-${m.id}-${r.code}`" @click="toggleReaction(m, r.code)"
              >{{ r.glyph }}</button>
            </div>
          </article>
          </template>
          <div ref="endRef" />
        </div>
      </ScrollArea>

      <footer class="border-t p-3">
        <p class="h-4 px-1 text-xs text-muted-foreground" aria-live="polite">{{ typingText }}</p>
        <p v-if="isArchived" class="px-1 text-sm text-muted-foreground">{{ t('teamChat.manage.archivedNotice') }}</p>
        <div v-if="!isArchived && canWrite && replyTo" class="mb-2 flex items-center gap-2 rounded-md border-l-2 border-primary/50 bg-muted/60 px-2 py-1 text-xs" data-testid="reply-bar">
          <CornerUpLeft class="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span class="min-w-0 flex-1 truncate">{{ t('teamChat.replyingTo', { name: replyTo.user_name }) }}: {{ replyTo.body }}</span>
          <button type="button" class="rounded p-1.5 text-muted-foreground hover:bg-muted" :aria-label="t('teamChat.cancelReply')" @click="store.cancelReply(channelId)">
            <X class="h-3.5 w-3.5" />
          </button>
        </div>
        <form v-if="!isArchived && canWrite" class="flex items-end gap-2" @submit.prevent="submit">
          <Textarea
            id="team-chat-composer"
            v-model="draft"
            class="max-h-40 min-h-[40px] flex-1 resize-none text-sm"
            :rows="1"
            :placeholder="t('teamChat.composerPlaceholder')"
            :aria-label="t('teamChat.composerPlaceholder')"
            :title="t('teamChat.composerHint')"
            @keydown="onComposerKey"
            @input="onInput"
          />
          <Button type="submit" size="icon" :disabled="sending || !isSendableBody(draft)" :aria-label="t('teamChat.send')">
            <Loader2 v-if="sending" class="h-4 w-4 animate-spin" />
            <Send v-else class="h-4 w-4" />
          </Button>
        </form>
        <p v-else-if="!isArchived" class="px-1 text-sm text-muted-foreground">{{ t('teamChat.readOnly') }}</p>
        <p v-if="overLimit" class="mt-1 px-1 text-xs text-destructive">{{ t('teamChat.tooLong', { max: TEAM_CHAT_MAX_BODY }) }}</p>
      </footer>
    </template>

    <ChannelManagePanel
      v-if="channel?.kind === 'channel'"
      v-model:open="manageOpen"
      :channel-id="channelId"
      @left="emit('back')"
    />
  </section>
</template>
