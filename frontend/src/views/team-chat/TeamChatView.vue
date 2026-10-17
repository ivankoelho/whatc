<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { MessagesSquare } from 'lucide-vue-next'
import { EmptyState } from '@/components/ui/empty-state'
import ChannelSidebar from '@/components/team-chat/ChannelSidebar.vue'
import MessagePanel from '@/components/team-chat/MessagePanel.vue'
import NewChatDialog from '@/components/team-chat/NewChatDialog.vue'
import { useTeamChatStore } from '@/stores/teamChat'
import { useIsMobile } from '@/composables/useIsMobile'

// The route param is only a request: the channel is opened through the API, so
// a made-up or foreign id ends in the "no access" state, never in someone
// else's messages. Access is decided by the server on every call.
const props = defineProps<{ channelId?: string }>()

const { t } = useI18n()
const router = useRouter()
const store = useTeamChatStore()
const isMobile = useIsMobile()

const dialogOpen = ref(false)
const dialogMode = ref<'channel' | 'dm'>('channel')

const activeId = computed(() => props.channelId ?? null)

onMounted(async () => {
  await Promise.all([store.fetchChannels(), store.fetchUsers()])
  if (activeId.value) await store.selectChannel(activeId.value)
})

watch(activeId, id => { void store.selectChannel(id) })

function open(id: string) {
  void router.push({ name: 'team-chat', params: { channelId: id } })
}

function openDialog(mode: 'channel' | 'dm') {
  dialogMode.value = mode
  dialogOpen.value = true
}

// On a phone one pane at a time: the list, or the open conversation.
const showSidebar = computed(() => !isMobile.value || !activeId.value)
const showPanel = computed(() => !isMobile.value || !!activeId.value)
</script>

<template>
  <div class="flex h-full min-h-0 w-full overflow-hidden">
    <ChannelSidebar
      v-show="showSidebar"
      :active-id="activeId"
      @select="open"
      @new-channel="openDialog('channel')"
      @new-dm="openDialog('dm')"
      @admin="router.push({ name: 'team-chat-admin' })"
    />

    <template v-if="showPanel">
      <MessagePanel
        v-if="activeId"
        :key="activeId"
        :channel-id="activeId"
        show-back
        @back="router.push({ name: 'team-chat' })"
      />
      <EmptyState
        v-else
        class="flex-1"
        :icon="MessagesSquare"
        :title="t('teamChat.pickTitle')"
        :description="t('teamChat.pickDescription')"
      />
    </template>

    <NewChatDialog v-model:open="dialogOpen" :mode="dialogMode" @created="open" />
  </div>
</template>
