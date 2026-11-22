<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import type { SalesOpportunity } from '@/services/api'
import { formatCurrency } from '@/lib/currency'
import { getInitials, getAvatarGradient, formatDate } from '@/lib/utils'
import { MessageCircle, AlertTriangle, Megaphone, CalendarClock, Sparkles } from 'lucide-vue-next'

// A compact, rectangular card: who it is, what they want, when the meeting is and where they came from. Everything
// else (editing, the X2 order, direcionamento, history, actions) lives in the popup that opens on click.
const props = defineProps<{ opportunity: SalesOpportunity; disabled?: boolean }>()
const emit = defineEmits<{ open: [opportunity: SalesOpportunity] }>()

const { locale } = useI18n()

const contactName = computed(() =>
  props.opportunity.contact?.profile_name || props.opportunity.contact?.phone_number || props.opportunity.contact_id)
const sellerName = computed(() => props.opportunity.assigned_user?.full_name || '')
const openedAt = computed(() => {
  const d = new Date(props.opportunity.opened_at)
  return Number.isNaN(d.getTime()) ? '' : formatDate(d)
})

// The meeting the customer booked: "ter., 24/11 09:00". Green when it is today.
const meetingWhen = computed(() => {
  const m = props.opportunity.meeting
  if (!m) return ''
  const d = new Date(m.start)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString(locale.value, { weekday: 'short', day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' })
})
const meetingIsToday = computed(() => {
  const m = props.opportunity.meeting
  return !!m && new Date(m.start).toDateString() === new Date().toDateString()
})

// One line of what the customer said: the first "- Label: value" lines of the AI's qualification (up to two, joined),
// or the first line of a plain interest text.
const summary = computed(() => {
  const text = props.opportunity.interest ?? ''
  if (!text.trim()) return ''
  const given: string[] = []
  let inGiven = false
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (/^informado pelo cliente/i.test(line)) { inGiven = true; continue }
    if (/^ainda n[aã]o coletado/i.test(line) || /^reuni[aã]o agendada/i.test(line)) { inGiven = false; continue }
    if (inGiven && line.startsWith('- ') && !/nada informado/i.test(line)) given.push(line.slice(2))
  }
  if (given.length) return given.slice(0, 2).join(' · ')
  return text.split('\n').map(l => l.trim()).find(l => l && !/^qualifica[cç][aã]o/i.test(l)) ?? ''
})
const hasValue = computed(() => props.opportunity.estimated_value != null && props.opportunity.estimated_value > 0)
</script>

<template>
  <div
    data-board-card
    role="button"
    tabindex="0"
    :aria-label="contactName"
    class="rounded-lg bg-card px-2.5 py-2 shadow-sm ring-1 ring-black/[0.05] transition-shadow hover:shadow-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring dark:ring-white/[0.07]"
    :class="disabled ? 'opacity-50 cursor-wait' : 'cursor-pointer active:cursor-grabbing'"
    @click="emit('open', opportunity)"
    @keydown.enter.prevent="emit('open', opportunity)"
    @keydown.space.prevent="emit('open', opportunity)"
  >
    <!-- Single root element on purpose: vuedraggable maps each item through the component's root node, so nothing
         (not even a comment) may sit BESIDE it, or dragging breaks. The whole card opens the popup; a drag does not click. -->
    <div class="flex items-center gap-2">
      <Avatar class="h-7 w-7 shrink-0">
        <AvatarFallback :class="'bg-gradient-to-br text-[10px] font-semibold text-white ' + getAvatarGradient(contactName)">
          {{ getInitials(contactName) }}
        </AvatarFallback>
      </Avatar>
      <p class="min-w-0 flex-1 truncate text-[13px] font-semibold leading-tight text-foreground">{{ contactName }}</p>
      <span
        v-if="opportunity.created_by_ai"
        data-testid="sales-opportunity-ai-badge"
        :title="$t('sales.aiBadgeTitle')"
        class="inline-flex shrink-0 items-center gap-0.5 rounded-full bg-violet-500/15 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-violet-700 dark:text-violet-300"
      ><Sparkles class="h-2.5 w-2.5" aria-hidden="true" />{{ $t('sales.aiBadge') }}</span>
      <span
        v-if="opportunity.sla_breached"
        data-testid="sales-opportunity-sla"
        :title="$t('sales.slaBreached')"
        class="inline-flex shrink-0 items-center rounded-full bg-red-500/15 p-0.5 text-red-700 dark:text-red-300"
      ><AlertTriangle class="h-3 w-3" aria-hidden="true" /></span>
      <RouterLink
        :to="`/chat/${opportunity.contact_id}`"
        :title="$t('chat.openConversation')"
        :aria-label="`${$t('chat.openConversation')}: ${contactName}`"
        class="shrink-0 rounded-md p-0.5 text-emerald-500 transition-colors hover:text-emerald-600 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        @click.stop
        @mousedown.stop
      ><MessageCircle class="h-3.5 w-3.5" aria-hidden="true" /></RouterLink>
    </div>

    <p v-if="summary" data-testid="sales-opportunity-summary" class="mt-1 truncate pl-9 text-xs text-muted-foreground" :title="summary">{{ summary }}</p>

    <div class="mt-1.5 flex items-center gap-1.5 pl-9 text-[11px]">
      <span
        v-if="opportunity.meeting && meetingWhen"
        data-testid="sales-opportunity-meeting"
        :title="opportunity.meeting.location || undefined"
        class="inline-flex min-w-0 shrink items-center gap-1 rounded-full px-1.5 py-0.5 font-medium"
        :class="meetingIsToday ? 'bg-emerald-500/20 text-emerald-700 dark:text-emerald-300' : 'bg-sky-500/15 text-sky-700 dark:text-sky-300'"
      ><CalendarClock class="h-3 w-3 shrink-0" aria-hidden="true" /><span class="truncate">{{ meetingWhen }}</span></span>
      <span
        v-if="opportunity.ad_origin"
        data-testid="sales-opportunity-ad-origin"
        :title="`${$t('sales.adOriginTitle')}: ${opportunity.ad_origin.headline || $t('sales.adOriginUnnamed')}`"
        class="inline-flex shrink-0 items-center rounded-full bg-amber-500/15 p-0.5 text-amber-800 dark:text-amber-300"
      ><Megaphone class="h-3 w-3" aria-hidden="true" /></span>
      <span v-if="hasValue" data-testid="sales-opportunity-value" class="truncate font-semibold text-foreground">{{ formatCurrency(opportunity.estimated_value) }}</span>
      <span class="ml-auto flex shrink-0 items-center gap-1.5 text-muted-foreground">
        <span v-if="openedAt">{{ openedAt }}</span>
        <Avatar class="h-4 w-4" :title="sellerName || $t('sales.noSeller')">
          <AvatarFallback :class="sellerName ? 'bg-gradient-to-br text-[8px] text-white ' + getAvatarGradient(sellerName) : 'bg-muted text-[8px]'">
            {{ sellerName ? getInitials(sellerName) : '?' }}
          </AvatarFallback>
        </Avatar>
      </span>
    </div>
  </div>
</template>
