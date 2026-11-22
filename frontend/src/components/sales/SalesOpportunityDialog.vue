<script setup lang="ts">
// The general popup of an opportunity: everything the compact card leaves out, and editable where it can be.
// It opens when a card is clicked. Saving sends only what changed (the same PUT .../details the old inline editors
// used); direcionamento changes at once; the X2 order has its own form; convert/lose are the existing actions.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { salesOpportunitiesService } from '@/services/api'
import type { SalesOpportunity, SalesOpportunityEvent, SalesDirecionamento } from '@/services/api'
import { SALES_UNITS, parseDecimalInput } from '@/lib/salesUnits'
import { formatMoneyInput } from '@/lib/currency'
import { getErrorMessage } from '@/lib/api-utils'
import { getInitials, getAvatarGradient, formatDate } from '@/lib/utils'
import MoneyInput from '@/components/sales/MoneyInput.vue'
import XProcessLinkForm from '@/components/sales/XProcessLinkForm.vue'
import { Loader2, MessageCircle, CalendarClock, Megaphone, Sparkles, AlertTriangle } from 'lucide-vue-next'

const props = defineProps<{ open: boolean; opportunity: SalesOpportunity }>()
const emit = defineEmits<{
  'update:open': [value: boolean]
  saved: [opportunity: SalesOpportunity]
  'direcionamento-change': [opportunity: SalesOpportunity]
  convert: [opportunity: SalesOpportunity]
  lose: [opportunity: SalesOpportunity]
}>()

const { t, locale } = useI18n()

const NONE = 'none' // a Select cannot hold an empty value
const isOpen = computed(() => props.opportunity.status === 'aberta')
const isConverted = computed(() => props.opportunity.status === 'convertida')
const contactName = computed(() =>
  props.opportunity.contact?.profile_name || props.opportunity.contact?.phone_number || props.opportunity.contact_id)
const sellerName = computed(() => props.opportunity.assigned_user?.full_name || '')
const stageLabel = computed(() => {
  const o = props.opportunity
  if (o.status === 'convertida') return t('sales.statusConvertida')
  if (o.status === 'perdida') return t('sales.statusPerdida')
  return t({ potencial: 'sales.stagePotencial', abrir_orcamento: 'sales.stageAbrirOrcamento', direcionada: 'sales.stageDirecionada' }[o.stage] ?? 'sales.stagePotencial')
})

// --- the editable fields ---------------------------------------------------------------------------------------------
const interest = ref('')
const estimatedQuantity = ref('')
const estimatedValue = ref('')
const unit = ref<string>(NONE)
const realizedValue = ref('')
const realizedQuantity = ref('')
const saving = ref(false)

const events = ref<SalesOpportunityEvent[]>([])
const eventsLoading = ref(false)

function fill() {
  const o = props.opportunity
  interest.value = o.interest ?? ''
  estimatedQuantity.value = o.estimated_quantity != null ? String(o.estimated_quantity) : ''
  estimatedValue.value = formatMoneyInput(o.estimated_value)
  unit.value = o.unit_of_measure || NONE
  realizedValue.value = formatMoneyInput(o.realized_value)
  realizedQuantity.value = o.realized_quantity != null ? String(o.realized_quantity) : ''
}

async function loadEvents() {
  eventsLoading.value = true
  try {
    const { data } = await salesOpportunitiesService.listEvents(props.opportunity.id)
    events.value = data.data.events ?? []
  } catch {
    events.value = [] // the history is context: its failure never blocks editing
  } finally {
    eventsLoading.value = false
  }
}

// immediate: the board mounts this popup already open (it only exists once a card was clicked), so there is no
// closed-to-open change to wait for.
watch(() => props.open, opened => {
  if (!opened) return
  fill()
  loadEvents()
}, { immediate: true })

async function save() {
  const o = props.opportunity
  const details: Record<string, unknown> = {}
  if (interest.value !== (o.interest ?? '') && !isConverted.value) details.interest = interest.value

  if (isConverted.value) {
    const rv = parseDecimalInput(realizedValue.value)
    const rq = parseDecimalInput(realizedQuantity.value)
    if (Number.isNaN(rv) || Number.isNaN(rq)) { toast.error(t('sales.quantityInvalid')); return }
    if (rv !== undefined && rv !== o.realized_value) details.realized_value = rv
    if (rq !== undefined && rq !== o.realized_quantity) details.realized_quantity = rq
  } else if (isOpen.value) {
    const eq = parseDecimalInput(estimatedQuantity.value)
    const ev = parseDecimalInput(estimatedValue.value)
    if (Number.isNaN(eq) || Number.isNaN(ev)) { toast.error(t('sales.quantityInvalid')); return }
    if (eq !== undefined && eq !== o.estimated_quantity) details.estimated_quantity = eq
    if (ev !== undefined && ev !== o.estimated_value) details.estimated_value = ev
    const newUnit = unit.value === NONE ? '' : unit.value
    if (newUnit !== (o.unit_of_measure || '')) details.unit_of_measure = newUnit
  }
  if (Object.keys(details).length === 0) { emit('update:open', false); return }

  saving.value = true
  try {
    const { data } = await salesOpportunitiesService.updateDetails(o.id, details)
    toast.success(t('sales.detailsSaved'))
    emit('saved', data.data)
    emit('update:open', false)
  } catch (e) {
    toast.error(getErrorMessage(e, t('sales.detailsSaveFailed')))
  } finally {
    saving.value = false
  }
}

// --- direcionamento (changes at once, like the old card) ----------------------------------------------------------------
const DIRECIONAMENTOS: SalesDirecionamento[] = ['visita', 'whatsapp']
const DIRECIONAMENTO_LABEL: Record<SalesDirecionamento, string> = { visita: 'sales.direcionamentoVisita', whatsapp: 'sales.direcionamentoWhatsApp' }
const changingDirecionamento = ref(false)
async function onDirecionamentoChange(value: unknown) {
  const d = value as SalesDirecionamento
  if (!d || d === props.opportunity.direcionamento) return
  changingDirecionamento.value = true
  try {
    const { data } = await salesOpportunitiesService.changeDirecionamento(props.opportunity.id, d)
    toast.success(t('sales.direcionamentoChanged'))
    emit('direcionamento-change', data.data)
  } catch (e) {
    toast.error(getErrorMessage(e, t('sales.direcionamentoChangeFailed')))
  } finally {
    changingDirecionamento.value = false
  }
}

// --- read-only context ---------------------------------------------------------------------------------------------------
const meetingWhen = computed(() => {
  const m = props.opportunity.meeting
  if (!m) return ''
  const d = new Date(m.start)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString(locale.value, { weekday: 'long', day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit' })
})
function when(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : `${formatDate(d)} ${d.toLocaleTimeString(locale.value, { hour: '2-digit', minute: '2-digit' })}`
}
function eventLabel(e: SalesOpportunityEvent): string {
  const key = `sales.event.${e.type}`
  const text = t(key)
  return text === key ? e.type : text
}
const canResolve = computed(() => isOpen.value && props.opportunity.stage === 'direcionada')
function resolve(kind: 'convert' | 'lose') {
  emit('update:open', false)
  if (kind === 'convert') emit('convert', props.opportunity)
  else emit('lose', props.opportunity)
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="max-h-[90vh] overflow-y-auto sm:max-w-2xl" data-testid="sales-opportunity-dialog" @click.stop>
      <DialogHeader>
        <div class="flex items-center gap-3">
          <Avatar class="h-10 w-10 shrink-0">
            <AvatarFallback :class="'bg-gradient-to-br text-sm font-semibold text-white ' + getAvatarGradient(contactName)">{{ getInitials(contactName) }}</AvatarFallback>
          </Avatar>
          <div class="min-w-0 flex-1">
            <DialogTitle class="flex items-center gap-2">
              <span class="truncate">{{ contactName }}</span>
              <span v-if="opportunity.created_by_ai" data-testid="dialog-ai-badge" class="inline-flex shrink-0 items-center gap-0.5 rounded-full bg-violet-500/15 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-violet-700 dark:text-violet-300"><Sparkles class="h-2.5 w-2.5" aria-hidden="true" />{{ $t('sales.aiBadge') }}</span>
            </DialogTitle>
            <DialogDescription class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
              <span class="font-mono">{{ opportunity.opportunity_number }}</span>
              <span data-testid="dialog-stage" class="rounded-full bg-muted px-2 py-0.5 text-[11px] font-medium text-foreground">{{ stageLabel }}</span>
              <span v-if="opportunity.sla_breached" class="inline-flex items-center gap-0.5 rounded-full bg-red-500/15 px-1.5 py-0.5 text-[11px] font-medium text-red-700 dark:text-red-300"><AlertTriangle class="h-3 w-3" aria-hidden="true" />{{ $t('sales.slaBreached') }}</span>
            </DialogDescription>
          </div>
          <RouterLink
            :to="`/chat/${opportunity.contact_id}`"
            class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border px-2.5 text-xs font-medium text-emerald-700 hover:bg-muted dark:text-emerald-300"
            data-testid="dialog-open-chat"
          ><MessageCircle class="h-3.5 w-3.5" aria-hidden="true" />{{ $t('chat.openConversation') }}</RouterLink>
        </div>
      </DialogHeader>

      <div class="space-y-4 py-1">
        <!-- Where it came from and when the meeting is -->
        <div v-if="opportunity.meeting || opportunity.ad_origin" class="grid gap-2 sm:grid-cols-2">
          <div v-if="opportunity.meeting && meetingWhen" data-testid="dialog-meeting" class="flex items-start gap-3 rounded-lg bg-sky-500/10 p-3 text-sky-900 dark:text-sky-100">
            <CalendarClock class="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
            <div class="min-w-0 text-sm leading-snug">
              <p class="text-xs font-medium opacity-80">{{ $t('sales.meetingLabel') }}</p>
              <p class="font-semibold capitalize">{{ meetingWhen }}</p>
              <p v-if="opportunity.meeting.location" class="truncate opacity-80">{{ opportunity.meeting.location }}</p>
            </div>
          </div>
          <div v-if="opportunity.ad_origin" data-testid="dialog-ad-origin" class="flex items-start gap-3 rounded-lg bg-amber-500/10 p-3 text-amber-950 dark:text-amber-100">
            <Megaphone class="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
            <div class="min-w-0 text-sm leading-snug">
              <p class="text-xs font-medium opacity-80">{{ $t('sales.adOriginTitle') }}<span v-if="opportunity.ad_origin.clicks > 1"> · {{ opportunity.ad_origin.clicks }}×</span></p>
              <p class="font-semibold">{{ opportunity.ad_origin.headline || $t('sales.adOriginUnnamed') }}</p>
            </div>
          </div>
        </div>

        <!-- What the customer wants: the AI's qualification (or what a person wrote), editable while open -->
        <div class="space-y-1.5">
          <Label for="sales-dialog-interest">{{ $t('sales.interest') }}</Label>
          <Textarea
            id="sales-dialog-interest"
            v-model="interest"
            data-testid="dialog-interest"
            :rows="6"
            :disabled="isConverted"
            :placeholder="$t('sales.interestPlaceholder')"
          />
        </div>

        <!-- Commercial -->
        <div v-if="isOpen" class="grid gap-3 sm:grid-cols-3">
          <div class="space-y-1.5">
            <Label>{{ $t('sales.columnEstimatedValue') }}</Label>
            <MoneyInput v-model="estimatedValue" data-testid="dialog-value" :placeholder="$t('sales.estimatedValuePlaceholder')" />
          </div>
          <div class="space-y-1.5">
            <Label>{{ $t('sales.estimatedQuantity') }}</Label>
            <Input v-model="estimatedQuantity" data-testid="dialog-quantity" type="text" inputmode="decimal" placeholder="0" />
          </div>
          <div class="space-y-1.5">
            <Label>{{ $t('sales.unit') }}</Label>
            <Select v-model="unit">
              <SelectTrigger data-testid="dialog-unit"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem :value="NONE">{{ $t('sales.unitNone') }}</SelectItem>
                <SelectItem v-for="u in SALES_UNITS" :key="u" :value="u">{{ $t('sales.units.' + u) }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
        <div v-else-if="isConverted" class="grid gap-3 sm:grid-cols-2">
          <div class="space-y-1.5">
            <Label>{{ $t('sales.realizedValue') }}</Label>
            <MoneyInput v-model="realizedValue" data-testid="dialog-realized-value" placeholder="0,00" />
            <p v-if="opportunity.conversion_source === 'xprocess'" class="text-[11px] text-muted-foreground">{{ $t('sales.realizedValueX2Note') }}</p>
          </div>
          <div class="space-y-1.5">
            <Label>{{ $t('sales.realizedQuantity') }}<span v-if="opportunity.unit_of_measure"> ({{ opportunity.unit_of_measure }})</span></Label>
            <Input v-model="realizedQuantity" data-testid="dialog-realized-quantity" type="text" inputmode="decimal" placeholder="0" />
          </div>
        </div>

        <div v-if="isOpen" class="grid items-stretch gap-3 sm:grid-cols-2">
          <div class="flex items-center justify-between gap-3 rounded-lg border p-3">
            <Label class="whitespace-nowrap text-sm font-medium">{{ $t('sales.direcionamento') }}</Label>
            <Select :model-value="opportunity.direcionamento" :disabled="changingDirecionamento" @update:model-value="onDirecionamentoChange">
              <SelectTrigger class="h-8 w-40" data-testid="dialog-direcionamento"><SelectValue :placeholder="$t('sales.direcionamentoPlaceholder')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="d in DIRECIONAMENTOS" :key="d" :value="d">{{ $t(DIRECIONAMENTO_LABEL[d]) }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div>
            <XProcessLinkForm :opportunity-id="opportunity.id" :editable="isOpen" />
          </div>
        </div>
        <XProcessLinkForm v-else :opportunity-id="opportunity.id" :editable="false" />

        <!-- Who and when -->
        <dl class="grid grid-cols-2 gap-x-4 gap-y-3 rounded-lg bg-muted/40 p-3 text-sm sm:grid-cols-4" data-testid="dialog-facts">
          <div class="min-w-0"><dt class="text-xs text-muted-foreground">{{ $t('sales.seller') }}</dt><dd class="truncate font-medium">{{ sellerName || $t('sales.noSeller') }}</dd></div>
          <div class="min-w-0"><dt class="text-xs text-muted-foreground">{{ $t('sales.openedAt') }}</dt><dd class="truncate">{{ when(opportunity.opened_at) }}</dd></div>
          <div class="min-w-0"><dt class="text-xs text-muted-foreground">{{ $t('sales.stageChangedAt') }}</dt><dd class="truncate">{{ when(opportunity.stage_changed_at) }}</dd></div>
          <div v-if="opportunity.contact?.phone_number" class="min-w-0"><dt class="text-xs text-muted-foreground">{{ $t('sales.phone') }}</dt><dd class="truncate tabular-nums">{{ opportunity.contact.phone_number }}</dd></div>
        </dl>

        <!-- History -->
        <div class="space-y-1.5">
          <h4 class="text-sm font-medium">{{ $t('sales.history') }}</h4>
          <div v-if="eventsLoading" class="flex justify-center py-2"><Loader2 class="h-4 w-4 animate-spin text-muted-foreground" aria-hidden="true" /></div>
          <ul v-else-if="events.length" data-testid="dialog-events" class="space-y-1.5 text-xs text-muted-foreground">
            <li v-for="e in events" :key="e.id" class="flex items-center gap-2"><span class="h-1.5 w-1.5 shrink-0 rounded-full bg-muted-foreground/50" aria-hidden="true" /><span class="min-w-0 flex-1 truncate text-foreground">{{ eventLabel(e) }}</span><span class="shrink-0 tabular-nums">{{ when(e.created_at) }}</span></li>
          </ul>
          <p v-else class="text-xs text-muted-foreground">{{ $t('sales.historyEmpty') }}</p>
        </div>
      </div>

      <div class="flex flex-wrap items-center justify-between gap-2 border-t pt-4">
        <div v-if="canResolve" class="flex gap-2">
          <Button variant="outline" size="sm" data-testid="dialog-convert" @click="resolve('convert')">{{ $t('sales.markConvertedShort') }}</Button>
          <Button variant="outline" size="sm" data-testid="dialog-lose" @click="resolve('lose')">{{ $t('sales.markLostShort') }}</Button>
        </div>
        <span v-else />
        <div class="flex gap-2">
          <Button variant="outline" @click="emit('update:open', false)">{{ $t('common.close') }}</Button>
          <Button v-if="isOpen || isConverted" :disabled="saving" data-testid="dialog-save" @click="save">
            <Loader2 v-if="saving" class="mr-2 h-4 w-4 animate-spin" />{{ $t('common.save') }}
          </Button>
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>
