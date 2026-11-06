<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import type { Occurrence } from '@/services/api'
import { slaStatus } from '@/lib/occurrence-sla'
import { getInitials, getAvatarGradient, formatDate } from '@/lib/utils'
import { MessageCircle, Clock, AlertTriangle, ArrowUp } from 'lucide-vue-next'

const props = defineProps<{ occurrence: Occurrence; disabled?: boolean }>()

const { t } = useI18n()

// As chaves do i18n são camelCase; não existe `priority_low`.
const PRIORITY_KEY = {
  low: 'occurrences.priorityLow',
  normal: 'occurrences.priorityNormal',
  high: 'occurrences.priorityHigh',
  urgent: 'occurrences.priorityUrgent',
} as const

// Só alta/urgente pedem atenção visual; baixa fica como texto discreto e
// `normal` é o padrão implícito.
const PRIORITY_TONE = {
  low: 'bg-muted text-muted-foreground',
  normal: '',
  high: 'bg-amber-500/15 text-amber-700 dark:text-amber-300',
  urgent: 'bg-red-500/15 text-red-700 dark:text-red-300',
} as const

const contactName = computed(() => props.occurrence.contact_name || props.occurrence.contact_phone || '—')
const assignee = computed(() => props.occurrence.assigned_user_name || '')
const sla = computed(() => slaStatus(props.occurrence))
const openedAt = computed(() => {
  const d = new Date(props.occurrence.opened_at)
  return Number.isNaN(d.getTime()) ? '' : formatDate(d)
})
</script>

<template>
  <div
    data-board-card
    class="group rounded-xl bg-card p-3 shadow-sm ring-1 ring-black/[0.05] transition-shadow hover:shadow-md dark:ring-white/[0.07]"
    :class="disabled ? 'opacity-50 cursor-wait' : 'cursor-grab active:cursor-grabbing'"
  >
    <!-- Contact-first card (Bitrix/Kommo deal card): who it is on top, what it
         is in the middle, who owns it and when at the bottom. The column header
         carries the stage colour, so the card itself stays neutral. -->
    <!-- Single root element on purpose: vuedraggable maps each item through
         the component's root node, so nothing (not even a comment) may sit
         beside it, or dragging breaks. -->
    <div class="flex items-start gap-2.5">
      <Avatar class="h-9 w-9 shrink-0">
        <AvatarFallback :class="'bg-gradient-to-br text-xs font-semibold text-white ' + getAvatarGradient(contactName)">
          {{ getInitials(contactName) }}
        </AvatarFallback>
      </Avatar>
      <div class="min-w-0 flex-1">
        <p class="truncate text-sm font-semibold text-foreground">{{ contactName }}</p>
        <!-- WhatsApp line opens the contact's conversation. .stop keeps the
             click from also opening the occurrence (the card's own click). -->
        <RouterLink
          :to="`/chat/${occurrence.contact_id}`"
          :title="t('chat.openConversation')"
          :aria-label="`${t('chat.openConversation')}: ${contactName}`"
          class="mt-0.5 inline-flex max-w-full items-center gap-1 rounded-md text-xs text-muted-foreground transition-colors hover:text-emerald-600 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring dark:hover:text-emerald-400"
          @click.stop
        >
          <MessageCircle class="h-3 w-3 shrink-0 text-emerald-500" aria-hidden="true" />
          <span class="truncate">{{ occurrence.contact_phone || t('chat.openConversation') }}</span>
        </RouterLink>
      </div>
      <span
        v-if="occurrence.priority !== 'normal'"
        class="inline-flex shrink-0 items-center gap-0.5 rounded-full px-2 py-0.5 text-[11px] font-medium"
        :class="PRIORITY_TONE[occurrence.priority]"
      >
        <ArrowUp v-if="occurrence.priority === 'high' || occurrence.priority === 'urgent'" class="h-3 w-3" aria-hidden="true" />
        {{ t(PRIORITY_KEY[occurrence.priority]) }}
      </span>
    </div>

    <p class="mt-2.5 line-clamp-2 text-[13px] leading-snug text-foreground/90">{{ occurrence.title }}</p>
    <p class="mt-1 font-mono text-[11px] text-muted-foreground">{{ occurrence.protocol_number }}</p>

    <div class="mt-3 flex items-center justify-between gap-2 border-t pt-2.5">
      <div class="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
        <Avatar class="h-5 w-5 shrink-0">
          <AvatarFallback
            :class="assignee ? 'bg-gradient-to-br text-[9px] text-white ' + getAvatarGradient(assignee) : 'bg-muted text-[9px]'"
          >
            {{ assignee ? getInitials(assignee) : '?' }}
          </AvatarFallback>
        </Avatar>
        <span class="truncate" :class="!assignee && 'italic'">{{ assignee || t('occurrences.unassigned') }}</span>
      </div>
      <div class="flex shrink-0 items-center gap-1.5 text-[11px]">
        <span
          v-if="sla === 'overdue'"
          class="inline-flex items-center gap-0.5 rounded-full bg-red-500/15 px-1.5 py-0.5 font-medium text-red-700 dark:text-red-300"
        >
          <AlertTriangle class="h-3 w-3" aria-hidden="true" />{{ t('occurrences.slaOverdue') }}
        </span>
        <span
          v-else-if="sla === 'on_time'"
          class="inline-flex items-center gap-0.5 rounded-full bg-emerald-500/15 px-1.5 py-0.5 font-medium text-emerald-700 dark:text-emerald-300"
        >
          <Clock class="h-3 w-3" aria-hidden="true" />{{ t('occurrences.slaOnTime') }}
        </span>
        <span v-if="openedAt" class="text-muted-foreground">{{ openedAt }}</span>
      </div>
    </div>
  </div>
</template>
