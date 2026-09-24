<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { salesOpportunitiesService } from '@/services/api'
import type { SalesOpportunity, SalesLossReason } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { Loader2 } from 'lucide-vue-next'

const props = defineProps<{ open: boolean; opportunity: SalesOpportunity | null }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; lost: [opportunity: SalesOpportunity] }>()

const { t } = useI18n()

// As 8 chaves fechadas de SalesLossReason (services/api.ts) — não um valor
// livre.
const LOSS_REASONS: SalesLossReason[] = [
  'cliente_desistiu', 'preco', 'prazo', 'indisponibilidade',
  'comprou_concorrente', 'sem_retorno', 'problema_comercial', 'outro',
]
const LOSS_REASON_LABEL_KEY: Record<SalesLossReason, string> = {
  cliente_desistiu: 'sales.lossReasonClienteDesistiu',
  preco: 'sales.lossReasonPreco',
  prazo: 'sales.lossReasonPrazo',
  indisponibilidade: 'sales.lossReasonIndisponibilidade',
  comprou_concorrente: 'sales.lossReasonComprouConcorrente',
  sem_retorno: 'sales.lossReasonSemRetorno',
  problema_comercial: 'sales.lossReasonProblemaComercial',
  outro: 'sales.lossReasonOutro',
}

const lossReason = ref<SalesLossReason | undefined>(undefined)
const lossNotes = ref('')
const isSubmitting = ref(false)

watch(() => props.open, isOpen => {
  if (isOpen) {
    lossReason.value = undefined
    lossNotes.value = ''
  }
})

function closeDialog() {
  emit('update:open', false)
}

async function submit() {
  if (!props.opportunity || !lossReason.value) return
  isSubmitting.value = true
  try {
    const { data } = await salesOpportunitiesService.lose(props.opportunity.id, lossReason.value, lossNotes.value.trim() || undefined)
    toast.success(t('sales.lossRecorded'))
    emit('lost', data.data)
    emit('update:open', false)
  } catch (e) {
    toast.error(getErrorMessage(e, t('sales.lossFailed')))
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{{ $t('sales.loseDialogTitle') }}</DialogTitle>
        <DialogDescription>{{ $t('sales.loseDialogDesc') }}</DialogDescription>
      </DialogHeader>
      <div class="space-y-4 py-4">
        <div class="space-y-2">
          <Label>{{ $t('sales.lossReason') }} <span class="text-destructive">*</span></Label>
          <Select v-model="lossReason">
            <SelectTrigger>
              <SelectValue :placeholder="$t('sales.lossReasonPlaceholder')" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="reason in LOSS_REASONS" :key="reason" :value="reason">
                {{ $t(LOSS_REASON_LABEL_KEY[reason]) }}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="space-y-2">
          <Label>{{ $t('sales.lossNotes') }}</Label>
          <Textarea v-model="lossNotes" :placeholder="$t('sales.lossNotesPlaceholder')" :rows="3" />
        </div>
      </div>
      <div class="flex justify-end gap-2">
        <Button variant="outline" @click="closeDialog">{{ $t('common.cancel') }}</Button>
        <Button variant="destructive" :disabled="!lossReason || isSubmitting" @click="submit">
          <Loader2 v-if="isSubmitting" class="h-4 w-4 mr-2 animate-spin" />
          {{ $t('sales.markLost') }}
        </Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
