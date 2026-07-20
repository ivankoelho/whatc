<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import type { Contact } from '@/stores/contacts'

const props = defineProps<{
  contact: Contact
  active: boolean
  formatTime: (value?: string) => string
  getInitials: (value: string) => string
  getAvatarGradient: (value: string) => string
}>()

const { t } = useI18n()

const statusLabel = computed(() => {
  switch (props.contact.contact_status) {
    case 'new': return t('chat.statusNew')
    case 'in_progress': return t('chat.statusInProgress')
    default: return t('chat.statusResolved')
  }
})

// Resolved conversations carry no bar — the absence of a signal is the signal.
const statusBarClass = computed(() => {
  switch (props.contact.contact_status) {
    case 'new': return 'bg-emerald-500'
    case 'in_progress': return 'bg-sky-400'
    default: return 'bg-transparent'
  }
})

const displayName = computed(() => props.contact.name || props.contact.phone_number)
const statusTitle = computed(() => t('chat.conversationStatus', { status: statusLabel.value }))
</script>

<template>
  <div
    data-testid="conversation-item"
    :class="[
      'relative flex items-center gap-2 px-3 py-2 cursor-pointer hover:bg-white/[0.04] light:hover:bg-gray-50 transition-colors',
      props.active && 'bg-white/[0.08] light:bg-gray-100'
    ]"
    :title="statusTitle"
  >
    <!-- 2px edge bar: the row already carries avatar, name, time, preview and
         the unread badge; a fifth element would not survive 320px. -->
    <span class="absolute left-0 top-0 bottom-0 w-0.5" :class="statusBarClass" :aria-label="statusTitle" />
    <Avatar class="h-9 w-9 ring-2 ring-white/[0.1] light:ring-gray-200">
      <AvatarImage :src="props.contact.avatar_url" />
      <AvatarFallback :class="'text-xs bg-gradient-to-br text-white ' + props.getAvatarGradient(displayName)">
        {{ props.getInitials(displayName) }}
      </AvatarFallback>
    </Avatar>
    <div class="flex-1 min-w-0">
      <div class="flex items-center justify-between gap-2">
        <p
          class="flex-1 min-w-0 text-sm font-medium truncate text-white light:text-gray-900"
          :title="displayName"
        >
          {{ displayName }}
        </p>
        <span class="flex-shrink-0 text-[11px] text-white/40 light:text-gray-500">
          {{ props.formatTime(props.contact.last_message_at) }}
        </span>
      </div>
      <div class="flex items-center justify-between gap-2">
        <!-- Falls back to the phone number when there is no preview yet, so the
             second line is never empty. -->
        <p class="flex-1 min-w-0 text-xs text-white/50 light:text-gray-500 truncate">
          {{ props.contact.last_message_preview || props.contact.phone_number }}
        </p>
        <Badge
          v-if="props.contact.unread_count > 0"
          class="flex-shrink-0 h-5 text-[10px] bg-emerald-500/20 text-emerald-400 light:bg-emerald-100 light:text-emerald-700"
        >
          {{ props.contact.unread_count }}
        </Badge>
      </div>
    </div>
  </div>
</template>
