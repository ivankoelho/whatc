<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount, nextTick } from 'vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { occurrenceChatService } from '@/services/api'
import { useTeamChatStore } from '@/stores/teamChat'
import { useAuthStore } from '@/stores/auth'
import MessageComposer from '@/components/team-chat/MessageComposer.vue'
import MessageAttachments from '@/components/team-chat/MessageAttachments.vue'
import { bubbleClass, bubbleMeta, dayLabel, isOwnMessage } from '@/lib/chat-bubbles'
import { useI18n } from 'vue-i18n'
import { usePendingPoll } from '@/composables/usePendingPoll'
import { ArrowDown, MessagesSquare, Lock } from 'lucide-vue-next'

// The case's INTERNAL discussion: colleagues only. It reuses the internal chat
// (store, composer, attachments, realtime); the server decides who may read and
// write from the case itself. Nothing typed here goes to the customer: that is
// the WhatsApp card, with its own button and its own endpoint.
const props = defineProps<{ occurrenceId: string }>()
const { t, locale } = useI18n()
const timeOf = (iso: string) => new Date(iso).toLocaleTimeString(locale.value, { hour: '2-digit', minute: '2-digit' })
const dayText = (iso: string) => dayLabel(iso, locale.value, { today: t('teamChat.today'), yesterday: t('teamChat.yesterday') })

const store = useTeamChatStore()
const auth = useAuthStore()

const channelId = ref('')
const canPost = ref(false)
const failed = ref(false)
const endRef = ref<HTMLElement | null>(null)

const panel = computed(() => (channelId.value ? store.panel(channelId.value) : null))
const messages = computed(() => (panel.value?.messages ?? []).filter(m => m.kind !== 'system'))
// An attachment still being checked for malware: refresh by itself until it is released.
usePendingPoll(
  () => messages.value.some(m => m.attachments?.some(a => a.scan_status === 'pending' || a.scan_status === 'error')),
  () => (channelId.value ? store.refreshLatest(channelId.value) : undefined)
)
const myId = computed(() => auth.user?.id)

async function open() {
  failed.value = false
  try {
    const res = await occurrenceChatService.open(props.occurrenceId)
    const body = res.data as any
    const state = body.data ?? body
    channelId.value = state.channel_id
    canPost.value = !!state.can_post
    // Makes it the active channel: the store then keeps it live from WebSocket events.
    await store.selectChannel(channelId.value)
  } catch {
    failed.value = true
  }
}

watch(() => props.occurrenceId, open, { immediate: true })
// Follow new messages only when already near the end (or when it is mine); never pull a reader of old
// history to the bottom. Otherwise count them and offer a jump.
const listRef = ref<HTMLElement | null>(null)
const atBottom = ref(true)
const unseen = ref(0)
function trackBottom() {
  const el = listRef.value
  if (!el) return
  atBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 80
  if (atBottom.value) unseen.value = 0
}
function toBottom() {
  unseen.value = 0
  atBottom.value = true
  void nextTick(() => endRef.value?.scrollIntoView?.({ block: 'nearest' }))
}
watch(() => messages.value.length, async (now, was) => {
  const last = messages.value[now - 1]
  if (!was || atBottom.value || (last && isOwnMessage(last, myId.value))) toBottom()
  else unseen.value += now - was
})
onBeforeUnmount(() => { if (store.activeChannelId === channelId.value) void store.selectChannel(null) })
</script>

<template>
  <Card data-testid="occurrence-chat" class="flex flex-col border-sky-500/30" :class="messages.length > 0 ? 'flex-1' : ''">
    <CardHeader class="pb-3 max-sm:px-2">
      <CardTitle class="text-sm font-medium flex items-center gap-2">
        <MessagesSquare class="h-4 w-4" aria-hidden="true" />
        {{ $t('occurrences.internalChat') }}
        <Badge variant="secondary" class="gap-1"><Lock class="h-3 w-3" aria-hidden="true" />{{ $t('occurrences.internalOnly') }}</Badge>
        <RouterLink v-if="channelId" :to="`/team-chat/${channelId}`" class="ml-auto text-xs font-normal text-primary hover:underline" data-testid="open-in-chat">
          {{ $t('occurrences.openInChat') }}
        </RouterLink>
      </CardTitle>
      <p class="text-xs text-muted-foreground max-sm:hidden">{{ $t('occurrences.internalChatHint') }}</p>
    </CardHeader>
    <CardContent class="flex flex-1 flex-col gap-3 max-sm:px-2">
      <p v-if="failed" class="text-sm text-destructive">{{ $t('occurrences.internalChatFailed') }}</p>
      <template v-else-if="channelId">
        <p v-if="panel?.loading && messages.length === 0" class="text-sm text-muted-foreground">…</p>
        <p v-else-if="messages.length === 0" class="text-sm text-muted-foreground">{{ $t('occurrences.internalChatEmpty') }}</p>
        <div v-else class="relative flex min-h-48 flex-1 basis-0 flex-col">
        <ul ref="listRef" class="min-h-0 flex-1 space-y-2 overflow-y-auto pr-1" data-testid="internal-messages" @scroll="trackBottom">
          <template v-for="(m, i) in messages" :key="m.id">
            <li v-if="bubbleMeta(messages, i).newDay" class="flex justify-center py-1">
              <span class="rounded-full bg-muted px-3 py-1 text-xs text-muted-foreground">{{ dayText(m.created_at) }}</span>
            </li>
            <li class="flex" :class="[isOwnMessage(m, myId) ? 'justify-end' : 'justify-start', bubbleMeta(messages, i).first ? 'mt-1.5' : '']" :data-own="isOwnMessage(m, myId)">
              <div class="min-w-0 max-w-[88%] rounded-2xl px-3 py-1.5 text-sm shadow-sm md:max-w-[min(36rem,70%)]" :class="bubbleClass(isOwnMessage(m, myId))">
                <p v-if="!isOwnMessage(m, myId) && bubbleMeta(messages, i).first" class="text-xs font-semibold text-primary">{{ m.user_name }}</p>
                <p v-if="m.body" class="whitespace-pre-wrap [overflow-wrap:anywhere]">{{ m.body }}</p>
                <MessageAttachments v-if="m.attachments?.length" :attachments="m.attachments" />
                <p class="mt-0.5 text-right text-[11px] text-muted-foreground"><time :datetime="m.created_at">{{ timeOf(m.created_at) }}</time></p>
              </div>
            </li>
          </template>
          <li ref="endRef" aria-hidden="true" />
        </ul>
        <button
          v-if="unseen > 0" type="button" data-testid="new-messages"
          class="absolute bottom-2 left-1/2 flex -translate-x-1/2 items-center gap-1.5 rounded-full bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground shadow-lg"
          @click="toBottom"
        >
          <ArrowDown class="h-3.5 w-3.5" aria-hidden="true" />{{ t('teamChat.newMessages', { n: unseen }, unseen) }}
        </button>
        </div>
        <MessageComposer v-if="canPost" :channel-id="channelId" :can-write="canPost" :archived="false" />
        <p v-else class="text-xs text-muted-foreground">{{ $t('occurrences.internalChatReadOnly') }}</p>
      </template>
    </CardContent>
  </Card>
</template>
