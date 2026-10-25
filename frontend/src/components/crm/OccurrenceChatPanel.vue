<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount, nextTick } from 'vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { occurrenceChatService } from '@/services/api'
import { useTeamChatStore } from '@/stores/teamChat'
import { useAuthStore } from '@/stores/auth'
import MessageComposer from '@/components/team-chat/MessageComposer.vue'
import MessageAttachments from '@/components/team-chat/MessageAttachments.vue'
import { formatDateTime } from '@/lib/utils'
import { MessagesSquare, Lock } from 'lucide-vue-next'

// The case's INTERNAL discussion: colleagues only. It reuses the internal chat
// (store, composer, attachments, realtime); the server decides who may read and
// write from the case itself. Nothing typed here goes to the customer: that is
// the WhatsApp card, with its own button and its own endpoint.
const props = defineProps<{ occurrenceId: string }>()

const store = useTeamChatStore()
const auth = useAuthStore()

const channelId = ref('')
const canPost = ref(false)
const failed = ref(false)
const endRef = ref<HTMLElement | null>(null)

const panel = computed(() => (channelId.value ? store.panel(channelId.value) : null))
const messages = computed(() => (panel.value?.messages ?? []).filter(m => m.kind !== 'system'))
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
watch(() => messages.value.length, async () => {
  await nextTick()
  endRef.value?.scrollIntoView?.({ block: 'nearest' })
})
onBeforeUnmount(() => { if (store.activeChannelId === channelId.value) void store.selectChannel(null) })
</script>

<template>
  <Card data-testid="occurrence-chat" class="border-sky-500/30">
    <CardHeader class="pb-3">
      <CardTitle class="text-sm font-medium flex items-center gap-2">
        <MessagesSquare class="h-4 w-4" aria-hidden="true" />
        {{ $t('occurrences.internalChat') }}
        <Badge variant="secondary" class="gap-1"><Lock class="h-3 w-3" aria-hidden="true" />{{ $t('occurrences.internalOnly') }}</Badge>
      </CardTitle>
      <p class="text-xs text-muted-foreground">{{ $t('occurrences.internalChatHint') }}</p>
    </CardHeader>
    <CardContent class="space-y-3">
      <p v-if="failed" class="text-sm text-destructive">{{ $t('occurrences.internalChatFailed') }}</p>
      <template v-else-if="channelId">
        <p v-if="panel?.loading && messages.length === 0" class="text-sm text-muted-foreground">…</p>
        <p v-else-if="messages.length === 0" class="text-sm text-muted-foreground">{{ $t('occurrences.internalChatEmpty') }}</p>
        <ul v-else class="max-h-80 space-y-2 overflow-y-auto pr-1" data-testid="internal-messages">
          <li v-for="m in messages" :key="m.id" class="flex" :class="m.user_id === myId ? 'justify-end' : 'justify-start'">
            <div class="max-w-[85%] rounded-lg px-3 py-2 text-sm" :class="m.user_id === myId ? 'bg-sky-500/15' : 'bg-muted'">
              <div class="mb-0.5 flex flex-wrap items-baseline gap-2 text-xs text-muted-foreground">
                <span class="font-medium text-foreground">{{ m.user_name }}</span>
                <span>{{ formatDateTime(m.created_at) }}</span>
              </div>
              <p v-if="m.body" class="whitespace-pre-wrap [overflow-wrap:anywhere]">{{ m.body }}</p>
              <MessageAttachments v-if="m.attachments?.length" :attachments="m.attachments" />
            </div>
          </li>
          <li ref="endRef" aria-hidden="true" />
        </ul>
        <MessageComposer v-if="canPost" :channel-id="channelId" :can-write="canPost" :archived="false" />
        <p v-else class="text-xs text-muted-foreground">{{ $t('occurrences.internalChatReadOnly') }}</p>
      </template>
    </CardContent>
  </Card>
</template>
