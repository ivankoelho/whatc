<script setup lang="ts">
// Edits an opportunity's quantity fields. What can be edited depends on its
// status, matching the backend: an OPEN opportunity takes the commercial
// expectation (estimated quantity, unit, estimated value); a CONVERTED one takes
// what actually happened (realized value and quantity). When X2 has the order,
// it overwrites realized_value on reconciliation.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { salesOpportunitiesService } from '@/services/api'
import type { SalesOpportunity } from '@/services/api'
import { SALES_UNITS, parseDecimalInput } from '@/lib/salesUnits'
import { getErrorMessage } from '@/lib/api-utils'
import { Loader2 } from 'lucide-vue-next'

const props = defineProps<{ open: boolean; opportunity: SalesOpportunity }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; saved: [opportunity: SalesOpportunity] }>()

const { t } = useI18n()

const NONE = 'none' // the Select cannot hold an empty value
const isConverted = computed(() => props.opportunity.status === 'convertida')

const estimatedQuantity = ref('')
const estimatedValue = ref('')
const unit = ref<string>(NONE)
const realizedValue = ref('')
const realizedQuantity = ref('')
const saving = ref(false)

watch(() => props.open, isOpen => {
  if (!isOpen) return
  const o = props.opportunity
  estimatedQuantity.value = o.estimated_quantity != null ? String(o.estimated_quantity) : ''
  estimatedValue.value = o.estimated_value != null ? String(o.estimated_value) : ''
  unit.value = o.unit_of_measure || NONE
  realizedValue.value = o.realized_value != null ? String(o.realized_value) : ''
  realizedQuantity.value = o.realized_quantity != null ? String(o.realized_quantity) : ''
})

async function save() {
  const o = props.opportunity
  const details: Record<string, unknown> = {}

  if (isConverted.value) {
    const rv = parseDecimalInput(realizedValue.value)
    const rq = parseDecimalInput(realizedQuantity.value)
    if (Number.isNaN(rv) || Number.isNaN(rq)) {
      toast.error(t('sales.quantityInvalid'))
      return
    }
    if (rv !== undefined && rv !== o.realized_value) details.realized_value = rv
    if (rq !== undefined && rq !== o.realized_quantity) details.realized_quantity = rq
  } else {
    const eq = parseDecimalInput(estimatedQuantity.value)
    const ev = parseDecimalInput(estimatedValue.value)
    if (Number.isNaN(eq) || Number.isNaN(ev)) {
      toast.error(t('sales.quantityInvalid'))
      return
    }
    if (eq !== undefined && eq !== o.estimated_quantity) details.estimated_quantity = eq
    if (ev !== undefined && ev !== o.estimated_value) details.estimated_value = ev
    const newUnit = unit.value === NONE ? '' : unit.value
    if (newUnit !== (o.unit_of_measure || '')) details.unit_of_measure = newUnit
  }

  if (Object.keys(details).length === 0) {
    emit('update:open', false)
    return
  }
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
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="sm:max-w-md" @click.stop>
      <DialogHeader>
        <DialogTitle>{{ $t('sales.editDetails') }} · {{ opportunity.opportunity_number }}</DialogTitle>
        <DialogDescription>{{ isConverted ? $t('sales.editRealizedDesc') : $t('sales.editEstimatedDesc') }}</DialogDescription>
      </DialogHeader>

      <div v-if="!isConverted" class="space-y-4 py-4">
        <div class="grid grid-cols-2 gap-3">
          <div class="space-y-2">
            <Label>{{ $t('sales.estimatedQuantity') }}</Label>
            <Input v-model="estimatedQuantity" data-testid="sales-details-estimated-quantity" type="text" inputmode="decimal" placeholder="0" />
          </div>
          <div class="space-y-2">
            <Label>{{ $t('sales.unit') }}</Label>
            <Select v-model="unit">
              <SelectTrigger data-testid="sales-details-unit"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem :value="NONE">{{ $t('sales.unitNone') }}</SelectItem>
                <SelectItem v-for="u in SALES_UNITS" :key="u" :value="u">{{ $t('sales.units.' + u) }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
        <div class="space-y-2">
          <Label>{{ $t('sales.columnEstimatedValue') }}</Label>
          <Input v-model="estimatedValue" type="text" inputmode="decimal" :placeholder="$t('sales.estimatedValuePlaceholder')" />
        </div>
      </div>

      <div v-else class="space-y-4 py-4">
        <div class="space-y-2">
          <Label>{{ $t('sales.realizedValue') }}</Label>
          <Input v-model="realizedValue" data-testid="sales-details-realized-value" type="text" inputmode="decimal" placeholder="0,00" />
          <p v-if="opportunity.conversion_source === 'xprocess'" class="text-[11px] text-muted-foreground">
            {{ $t('sales.realizedValueX2Note') }}
          </p>
        </div>
        <div class="space-y-2">
          <Label>{{ $t('sales.realizedQuantity') }}<span v-if="opportunity.unit_of_measure"> ({{ opportunity.unit_of_measure }})</span></Label>
          <Input v-model="realizedQuantity" data-testid="sales-details-realized-quantity" type="text" inputmode="decimal" placeholder="0" />
        </div>
      </div>

      <div class="flex justify-end gap-2">
        <Button variant="outline" @click="emit('update:open', false)">{{ $t('common.cancel') }}</Button>
        <Button :disabled="saving" data-testid="sales-details-save" @click="save">
          <Loader2 v-if="saving" class="h-4 w-4 mr-2 animate-spin" />
          {{ $t('common.save') }}
        </Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
