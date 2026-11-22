<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { salesOpportunitiesService } from '@/services/api'
import type { SalesOpportunity, SalesDirecionamento } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { formatCurrency, formatMoneyInput } from '@/lib/currency'
import MoneyInput from '@/components/sales/MoneyInput.vue'
import XProcessLinkForm from '@/components/sales/XProcessLinkForm.vue'
import SalesOpportunityDetailsDialog from '@/components/sales/SalesOpportunityDetailsDialog.vue'
import { formatQuantity, parseDecimalInput } from '@/lib/salesUnits'
import { getInitials, getAvatarGradient, formatDate } from '@/lib/utils'
import { Pencil, MessageCircle, AlertTriangle, Megaphone, CalendarClock, Sparkles } from 'lucide-vue-next'

const props = defineProps<{ opportunity: SalesOpportunity; disabled?: boolean }>()

const emit = defineEmits<{
  convert: [opportunity: SalesOpportunity]
  lose: [opportunity: SalesOpportunity]
  'direcionamento-change': [opportunity: SalesOpportunity]
  'details-change': [opportunity: SalesOpportunity]
}>()

const { t, locale } = useI18n()

const contactName = computed(() =>
  props.opportunity.contact?.profile_name || props.opportunity.contact?.phone_number || props.opportunity.contact_id)
const sellerName = computed(() => props.opportunity.assigned_user?.full_name || '')
const openedAt = computed(() => {
  const d = new Date(props.opportunity.opened_at)
  return Number.isNaN(d.getTime()) ? '' : formatDate(d)
})

// As 2 chaves fechadas de SalesDirecionamento (services/api.ts).
const DIRECIONAMENTO_OPTIONS: SalesDirecionamento[] = ['visita', 'whatsapp']
const DIRECIONAMENTO_LABEL_KEY: Record<SalesDirecionamento, string> = {
  visita: 'sales.direcionamentoVisita',
  whatsapp: 'sales.direcionamentoWhatsApp',
}

// --- what the AI agent and the ad brought, shown on the card -------------------------------------------------
// The meeting the customer booked, said the way people read a calendar: "ter., 24/11 09:00".
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
// "Informado pelo cliente:" lines of the qualification the AI wrote ("- Label: value"), at most three: the
// rest is in the details. Text that is not that format is shown as its first line.
const qualLines = computed<string[]>(() => {
  const text = props.opportunity.interest ?? ''
  if (!text.trim()) return []
  const out: string[] = []
  let inGiven = false
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (/^informado pelo cliente/i.test(line)) { inGiven = true; continue }
    if (/^ainda n[aã]o coletado/i.test(line) || /^reuni[aã]o agendada/i.test(line)) { inGiven = false; continue }
    if (inGiven && line.startsWith('- ') && !/nada informado/i.test(line)) out.push(line.slice(2))
  }
  if (out.length) return out.slice(0, 3)
  const first = text.split('\n').map(l => l.trim()).find(l => l && !/^qualifica[cç][aã]o/i.test(l))
  return first ? [first] : []
})
// The commercial block (value, quantity, X2 order, direcionamento) is the store's. A card the AI opened for a
// service business keeps it out of the way until somebody uses it.
const showCommercial = computed(() => {
  const o = props.opportunity
  if (!o.created_by_ai) return true
  return o.estimated_value != null || o.estimated_quantity != null || !!o.direcionamento || !!o.xprocess_num_pedido || o.stage !== 'potencial' || o.status !== 'aberta'
})

const changingDirecionamento = ref(false)
const detailsOpen = ref(false)

async function onDirecionamentoChange(value: unknown) {
  const direcionamento = value as SalesDirecionamento
  if (!direcionamento || direcionamento === props.opportunity.direcionamento) return
  changingDirecionamento.value = true
  try {
    const { data } = await salesOpportunitiesService.changeDirecionamento(props.opportunity.id, direcionamento)
    toast.success(t('sales.direcionamentoChanged'))
    emit('direcionamento-change', data.data)
  } catch (e) {
    toast.error(getErrorMessage(e, t('sales.direcionamentoChangeFailed')))
  } finally {
    changingDirecionamento.value = false
  }
}

// Click-to-edit for estimated_value — the one field with real downstream
// impact (the "Valor estimado do funil" dashboard widget, finding 3).
// interest/estimated_quantity share the write path (PUT .../details) but
// aren't worth their own inline editors in this first cut.
const editingValue = ref(false)
const valueDraft = ref('')
const savingValue = ref(false)

function startEditValue() {
  if (props.opportunity.status !== 'aberta') return
  valueDraft.value = formatMoneyInput(props.opportunity.estimated_value)
  editingValue.value = true
}

async function saveValue() {
  editingValue.value = false
  const raw = valueDraft.value.trim()
  // Clearing the field back to empty isn't a supported "unset" here (the
  // details endpoint only ever sets fields it's given, spec §4 has no
  // requirement to unset) — treat it as a no-op cancel instead of erroring.
  if (raw === '') return
  const parsed = parseDecimalInput(raw)
  if (parsed === undefined || Number.isNaN(parsed)) {
    toast.error(t('sales.estimatedValueInvalid'))
    return
  }
  if (parsed === props.opportunity.estimated_value) return
  savingValue.value = true
  try {
    const { data } = await salesOpportunitiesService.updateDetails(props.opportunity.id, { estimated_value: parsed })
    toast.success(t('sales.estimatedValueChanged'))
    emit('details-change', data.data)
  } catch (e) {
    toast.error(getErrorMessage(e, t('sales.estimatedValueChangeFailed')))
  } finally {
    savingValue.value = false
  }
}
</script>

<template>
  <div
    data-board-card
    class="rounded-xl bg-card p-3 shadow-sm ring-1 ring-black/[0.05] transition-shadow hover:shadow-md dark:ring-white/[0.07]"
    :class="disabled ? 'opacity-50 cursor-wait' : 'cursor-grab active:cursor-grabbing'"
  >
    <!-- Contact-first card, same layout as the CRM board's OccurrenceCard: who
         on top, the deal and its actions in the middle, seller + date below. -->
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
        <p class="flex items-center gap-1.5 text-sm font-semibold text-foreground">
          <span class="truncate">{{ contactName }}</span>
          <span
            v-if="opportunity.created_by_ai"
            data-testid="sales-opportunity-ai-badge"
            :title="$t('sales.aiBadgeTitle')"
            class="inline-flex shrink-0 items-center gap-0.5 rounded-full bg-violet-500/15 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-violet-700 dark:text-violet-300"
          ><Sparkles class="h-2.5 w-2.5" aria-hidden="true" />{{ $t('sales.aiBadge') }}</span>
        </p>
        <!-- WhatsApp line opens the contact's conversation -->
        <RouterLink
          :to="`/chat/${opportunity.contact_id}`"
          :title="$t('chat.openConversation')"
          :aria-label="`${$t('chat.openConversation')}: ${contactName}`"
          class="mt-0.5 inline-flex max-w-full items-center gap-1 rounded-md text-xs text-muted-foreground transition-colors hover:text-emerald-600 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring dark:hover:text-emerald-400"
          @click.stop
          @mousedown.stop
        >
          <MessageCircle class="h-3 w-3 shrink-0 text-emerald-500" aria-hidden="true" />
          <span class="truncate">{{ opportunity.contact?.phone_number || $t('chat.openConversation') }}</span>
        </RouterLink>
      </div>
      <Button
        v-if="opportunity.status === 'aberta'"
        data-testid="sales-opportunity-edit-details"
        variant="ghost"
        size="icon"
        class="-mr-1 -mt-1 h-7 w-7 shrink-0 text-muted-foreground"
        :title="$t('sales.editDetails')"
        :aria-label="$t('sales.editDetails')"
        @click.stop="detailsOpen = true"
        @mousedown.stop
      >
        <Pencil class="h-3.5 w-3.5" />
      </Button>
    </div>

    <!-- The meeting the customer booked and the ad that brought them -->
    <div v-if="opportunity.meeting || opportunity.ad_origin" class="mt-2 flex flex-wrap gap-1.5">
      <span
        v-if="opportunity.meeting && meetingWhen"
        data-testid="sales-opportunity-meeting"
        :title="opportunity.meeting.location || undefined"
        class="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium"
        :class="meetingIsToday ? 'bg-emerald-500/20 text-emerald-700 dark:text-emerald-300' : 'bg-sky-500/15 text-sky-700 dark:text-sky-300'"
      ><CalendarClock class="h-3 w-3" aria-hidden="true" />{{ meetingWhen }}</span>
      <span
        v-if="opportunity.ad_origin"
        data-testid="sales-opportunity-ad-origin"
        :title="$t('sales.adOriginTitle')"
        class="inline-flex max-w-full items-center gap-1 rounded-full bg-amber-500/15 px-2 py-0.5 text-[11px] font-medium text-amber-800 dark:text-amber-300"
      ><Megaphone class="h-3 w-3 shrink-0" aria-hidden="true" /><span class="truncate">{{ opportunity.ad_origin.headline || $t('sales.adOriginUnnamed') }}</span></span>
    </div>
    <ul v-if="qualLines.length" data-testid="sales-opportunity-qualification" class="mt-2 space-y-0.5 text-xs text-muted-foreground">
      <li v-for="(line, i) in qualLines" :key="i" class="truncate" :title="line">{{ line }}</li>
    </ul>

    <template v-if="showCommercial">
    <div class="mt-2.5 flex items-baseline justify-between gap-2">
      <div v-if="editingValue" class="flex-1" @click.stop @mousedown.stop>
        <MoneyInput
          v-model="valueDraft"
          data-testid="sales-opportunity-value-input"
          class="h-7 text-xs"
          autofocus
          @keyup.enter="saveValue"
          @keyup.escape="editingValue = false"
          @blur="saveValue"
        />
      </div>
      <p
        v-else
        data-testid="sales-opportunity-value"
        class="truncate text-[15px] font-semibold text-foreground"
        :class="{ 'cursor-text hover:underline': opportunity.status === 'aberta', 'opacity-50': savingValue }"
        @click.stop="startEditValue"
      >
        {{ formatCurrency(opportunity.estimated_value) }}
      </p>
      <span class="shrink-0 font-mono text-[11px] text-muted-foreground">{{ opportunity.opportunity_number }}</span>
    </div>
    <p v-if="opportunity.estimated_quantity != null" data-testid="sales-opportunity-quantity" class="mt-0.5 text-xs text-muted-foreground">
      {{ formatQuantity(opportunity.estimated_quantity, opportunity.unit_of_measure) }}
    </p>
    <p v-if="opportunity.status === 'convertida' && opportunity.conversion_source" class="mt-0.5 text-xs text-muted-foreground">
      {{ opportunity.conversion_source === 'xprocess' ? $t('sales.conversionSourceXProcess') : $t('sales.conversionSourceManual') }}
    </p>

    <div class="mt-2" @click.stop @mousedown.stop>
      <XProcessLinkForm :opportunity-id="opportunity.id" :editable="opportunity.status === 'aberta'" />
    </div>
    <div v-if="opportunity.status === 'aberta'" class="mt-2" @click.stop @mousedown.stop>
      <Select
        data-testid="sales-opportunity-direcionamento-select"
        :model-value="opportunity.direcionamento"
        :disabled="changingDirecionamento"
        @update:model-value="onDirecionamentoChange"
      >
        <SelectTrigger class="h-7 text-xs">
          <SelectValue :placeholder="$t('sales.direcionamentoPlaceholder')" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem v-for="option in DIRECIONAMENTO_OPTIONS" :key="option" :value="option">
            {{ $t(DIRECIONAMENTO_LABEL_KEY[option]) }}
          </SelectItem>
        </SelectContent>
      </Select>
    </div>
    </template>
    <p v-else class="mt-2.5 text-right font-mono text-[11px] text-muted-foreground">{{ opportunity.opportunity_number }}</p>
    <div v-if="opportunity.status === 'aberta' && opportunity.stage === 'direcionada'" class="mt-2 flex gap-2">
      <Button data-testid="sales-opportunity-convert-button" size="sm" variant="outline" class="flex-1 text-xs px-1" @click.stop="$emit('convert', opportunity)">
        {{ $t('sales.markConvertedShort') }}
      </Button>
      <Button data-testid="sales-opportunity-lose-button" size="sm" variant="outline" class="flex-1 text-xs px-1" @click.stop="$emit('lose', opportunity)">
        {{ $t('sales.markLostShort') }}
      </Button>
    </div>

    <div class="mt-3 flex items-center justify-between gap-2 border-t pt-2.5">
      <div class="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
        <Avatar class="h-5 w-5 shrink-0">
          <AvatarFallback :class="sellerName ? 'bg-gradient-to-br text-[9px] text-white ' + getAvatarGradient(sellerName) : 'bg-muted text-[9px]'">
            {{ sellerName ? getInitials(sellerName) : '?' }}
          </AvatarFallback>
        </Avatar>
        <span class="truncate" :class="!sellerName && 'italic'">{{ sellerName || $t('sales.noSeller') }}</span>
      </div>
      <div class="flex shrink-0 items-center gap-1.5 text-[11px]">
        <span
          v-if="opportunity.sla_breached"
          class="inline-flex items-center gap-0.5 rounded-full bg-red-500/15 px-1.5 py-0.5 font-medium text-red-700 dark:text-red-300"
        >
          <AlertTriangle class="h-3 w-3" aria-hidden="true" />{{ $t('sales.slaBreached') }}
        </span>
        <span v-if="openedAt" class="text-muted-foreground">{{ openedAt }}</span>
      </div>
    </div>
    <SalesOpportunityDetailsDialog v-model:open="detailsOpen" :opportunity="opportunity" @saved="$emit('details-change', $event)" />
  </div>
</template>
