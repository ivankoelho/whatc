<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { salesOpportunitiesService } from '@/services/api'
import type { SalesOpportunity } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { Loader2 } from 'lucide-vue-next'

const props = defineProps<{ open: boolean; contactId: string }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; created: [opportunity: SalesOpportunity] }>()

const { t } = useI18n()

const interest = ref('')
const estimatedValue = ref('')
const isSubmitting = ref(false)

watch(() => props.open, isOpen => {
  if (isOpen) {
    interest.value = ''
    estimatedValue.value = ''
  }
})

function closeDialog() {
  emit('update:open', false)
}

async function submit() {
  let value: number | undefined
  if (estimatedValue.value.trim() !== '') {
    value = Number(estimatedValue.value)
    if (Number.isNaN(value)) {
      toast.error(t('sales.estimatedValueInvalid'))
      return
    }
  }

  isSubmitting.value = true
  try {
    const { data } = await salesOpportunitiesService.create({
      contact_id: props.contactId,
      interest: interest.value.trim() || undefined,
      estimated_value: value,
    })
    toast.success(t('sales.opportunityCreated'))
    emit('created', data.data)
    emit('update:open', false)
  } catch (e) {
    toast.error(getErrorMessage(e, t('sales.opportunityCreateFailed')))
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{{ $t('sales.createDialogTitle') }}</DialogTitle>
        <DialogDescription>{{ $t('sales.createDialogDesc') }}</DialogDescription>
      </DialogHeader>
      <div class="space-y-4 py-4">
        <div class="space-y-2">
          <Label>{{ $t('sales.interest') }}</Label>
          <Textarea v-model="interest" :placeholder="$t('sales.interestPlaceholder')" :rows="3" />
        </div>
        <div class="space-y-2">
          <Label>{{ $t('sales.columnEstimatedValue') }}</Label>
          <Input v-model="estimatedValue" type="text" inputmode="decimal" :placeholder="$t('sales.estimatedValuePlaceholder')" />
        </div>
      </div>
      <div class="flex justify-end gap-2">
        <Button variant="outline" @click="closeDialog">{{ $t('common.cancel') }}</Button>
        <Button :disabled="isSubmitting" @click="submit">
          <Loader2 v-if="isSubmitting" class="h-4 w-4 mr-2 animate-spin" />
          {{ $t('sales.createOpportunity') }}
        </Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
