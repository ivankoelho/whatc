<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Input } from '@/components/ui/input'
import { salesOpportunitiesService } from '@/services/api'
import type { SalesOpportunity, SalesDirecionamento } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { formatCurrency } from '@/lib/currency'
import XProcessLinkForm from '@/components/sales/XProcessLinkForm.vue'
import SalesOpportunityDetailsDialog from '@/components/sales/SalesOpportunityDetailsDialog.vue'
import { formatQuantity } from '@/lib/salesUnits'
import { Pencil } from 'lucide-vue-next'

const props = defineProps<{ opportunity: SalesOpportunity; disabled?: boolean }>()

const emit = defineEmits<{
  convert: [opportunity: SalesOpportunity]
  lose: [opportunity: SalesOpportunity]
  'direcionamento-change': [opportunity: SalesOpportunity]
  'details-change': [opportunity: SalesOpportunity]
}>()

const { t } = useI18n()

// As 2 chaves fechadas de SalesDirecionamento (services/api.ts).
const DIRECIONAMENTO_OPTIONS: SalesDirecionamento[] = ['visita', 'whatsapp']
const DIRECIONAMENTO_LABEL_KEY: Record<SalesDirecionamento, string> = {
  visita: 'sales.direcionamentoVisita',
  whatsapp: 'sales.direcionamentoWhatsApp',
}

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
  valueDraft.value = props.opportunity.estimated_value != null ? String(props.opportunity.estimated_value) : ''
  editingValue.value = true
}

async function saveValue() {
  editingValue.value = false
  const raw = valueDraft.value.trim()
  // Clearing the field back to empty isn't a supported "unset" here (the
  // details endpoint only ever sets fields it's given, spec §4 has no
  // requirement to unset) — treat it as a no-op cancel instead of erroring.
  if (raw === '') return
  const parsed = Number(raw)
  if (Number.isNaN(parsed)) {
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
    class="rounded-md border border-white/[0.08] light:border-gray-200 bg-white/[0.02] light:bg-white p-3"
    :class="disabled ? 'opacity-50 cursor-wait' : 'cursor-grab'"
  >
    <div class="flex items-center justify-between gap-2">
      <span class="font-mono text-xs text-white/50 light:text-muted-foreground">
        {{ opportunity.opportunity_number }}
      </span>
      <Badge v-if="opportunity.sla_breached" variant="destructive" class="shrink-0 text-xs">
        {{ $t('sales.slaBreached') }}
      </Badge>
      <Button
        v-if="opportunity.status === 'aberta'"
        data-testid="sales-opportunity-edit-details"
        variant="ghost"
        size="icon"
        class="h-6 w-6 shrink-0"
        :title="$t('sales.editDetails')"
        @click.stop="detailsOpen = true"
        @mousedown.stop
      >
        <Pencil class="h-3 w-3" />
      </Button>
    </div>
    <p class="text-xs mt-1 truncate text-white/50 light:text-muted-foreground">
      <span class="text-white/30 light:text-gray-400">{{ $t('sales.contactLabel') }}:</span>
      {{ opportunity.contact?.profile_name || opportunity.contact_id }}
    </p>
    <div v-if="editingValue" class="mt-1" @click.stop @mousedown.stop>
      <Input
        v-model="valueDraft"
        data-testid="sales-opportunity-value-input"
        type="number"
        step="0.01"
        min="0"
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
      class="text-sm mt-1 truncate text-white light:text-gray-900"
      :class="{ 'cursor-text hover:underline': opportunity.status === 'aberta', 'opacity-50': savingValue }"
      @click.stop="startEditValue"
    >
      {{ formatCurrency(opportunity.estimated_value) }}
    </p>
    <p v-if="opportunity.estimated_quantity != null" data-testid="sales-opportunity-quantity" class="text-xs mt-0.5 text-white/60 light:text-muted-foreground">
      {{ formatQuantity(opportunity.estimated_quantity, opportunity.unit_of_measure) }}
    </p>
    <p v-if="opportunity.status === 'convertida' && opportunity.conversion_source" class="text-xs mt-0.5 text-white/40 light:text-muted-foreground">
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
    <div v-if="opportunity.status === 'aberta' && opportunity.stage === 'direcionada'" class="mt-2 flex gap-2">
      <Button data-testid="sales-opportunity-convert-button" size="sm" variant="outline" class="flex-1 text-xs px-1" @click.stop="$emit('convert', opportunity)">
        {{ $t('sales.markConvertedShort') }}
      </Button>
      <Button data-testid="sales-opportunity-lose-button" size="sm" variant="outline" class="flex-1 text-xs px-1" @click.stop="$emit('lose', opportunity)">
        {{ $t('sales.markLostShort') }}
      </Button>
    </div>
    <SalesOpportunityDetailsDialog v-model:open="detailsOpen" :opportunity="opportunity" @saved="$emit('details-change', $event)" />
  </div>
</template>
