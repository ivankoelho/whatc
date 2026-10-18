<script setup lang="ts">
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import ChannelComposition from './ChannelComposition.vue'
import { useTeamChatStore } from '@/stores/teamChat'
import { useAuthStore } from '@/stores/auth'

// The side panel of a free channel the user is IN: its members and what the user may
// do about them. Members only: someone who manages the composition without being in
// the channel uses the administration screen, which never shows messages.
const props = defineProps<{ open: boolean; channelId: string }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; left: [] }>()

const { t } = useI18n()
const store = useTeamChatStore()
const auth = useAuthStore()

const channel = computed(() => store.channelById(props.channelId))
const members = computed(() => store.members[props.channelId] ?? [])
const canManage = computed(() => auth.hasPermission('team_chat', 'manage'))
const canCompose = computed(() => channel.value?.my_role === 'owner' || canManage.value)

watch(() => props.open, open => { if (open) void store.fetchMembers(props.channelId) }, { immediate: true })

function onLeft() {
  emit('update:open', false)
  emit('left')
}
</script>

<template>
  <Sheet :open="open" @update:open="emit('update:open', $event)">
    <SheetContent side="right" class="w-full overflow-y-auto sm:max-w-md">
      <SheetHeader>
        <SheetTitle>{{ t('teamChat.manage.title') }}</SheetTitle>
        <SheetDescription>{{ t('teamChat.manage.desc') }}</SheetDescription>
      </SheetHeader>
      <div class="mt-4">
        <ChannelComposition
          v-if="channel"
          :channel-id="channelId"
          :name="channel.name"
          :archived-at="channel.archived_at"
          :members="members"
          :can-compose="canCompose"
          :can-reopen="canManage"
          :can-join="false"
          :is-member="true"
          :me-id="auth.user?.id"
          @left="onLeft"
        />
      </div>
    </SheetContent>
  </Sheet>
</template>
