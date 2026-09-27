<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { salesOpportunityXProcessLinkService, type SalesOpportunityXProcessLink } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { Loader2 } from 'lucide-vue-next'

// Split out of XProcessLinkForm so the Kanban card only ever renders a
// compact summary — the two-field form needs real width (design §6's "agent
// corrects a typo" flow needs both fields fully visible, not squeezed into a
// ~250px card column), which a Dialog gives it without widening the card.
const props = defineProps<{
  open: boolean
  opportunityId: string
  link: SalesOpportunityXProcessLink | null
  // Spec item G: prefills from a discovered "esse cliente comprou
  // recentemente" candidate instead of an existing link — takes priority
  // over `link` when set, since a candidate is a fresh find, not an edit.
  prefill?: { numPedido: string; documento: string } | null
}>()
const emit = defineEmits<{ 'update:open': [value: boolean]; registered: [] }>()

const { t } = useI18n()
const numPedido = ref('')
const documento = ref('')
const saving = ref(false)

// A link still open (resolved_at nulo) is editable in place (design §6) —
// prefill what's already registered. A resolved link is immutable, so this
// dialog is for a NEW vínculo instead: start blank rather than showing
// historical data as if it were still editable.
watch(() => props.open, isOpen => {
  if (!isOpen) return
  if (props.prefill) {
    numPedido.value = props.prefill.numPedido
    documento.value = props.prefill.documento
  } else if (props.link && !props.link.resolved_at) {
    numPedido.value = props.link.num_pedido ?? ''
    documento.value = props.link.documento ?? ''
  } else {
    numPedido.value = ''
    documento.value = ''
  }
})

function close() {
  emit('update:open', false)
}

async function register() {
  if (!numPedido.value || !documento.value) return
  saving.value = true
  try {
    await salesOpportunityXProcessLinkService.upsert(props.opportunityId, {
      num_pedido: numPedido.value, documento: documento.value,
    })
    toast.success(t('xprocessLink.registered'))
    emit('registered')
    emit('update:open', false)
  } catch (e) {
    toast.error(getErrorMessage(e, t('xprocessLink.registerFailure')))
  } finally {
    saving.value = false
  }
}

const isNewAfterResolved = () => !!(props.link && props.link.resolved_at)
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{{ t('xprocessLink.title') }}</DialogTitle>
        <DialogDescription>{{ t('xprocessLink.dialogDescription') }}</DialogDescription>
      </DialogHeader>
      <div class="space-y-4 py-2">
        <div class="space-y-2">
          <Label>{{ isNewAfterResolved() ? t('xprocessLink.numPedidoNew') : t('xprocessLink.numPedido') }}</Label>
          <Input v-model="numPedido" autofocus />
        </div>
        <div class="space-y-2">
          <Label>{{ t('xprocessLink.documento') }}</Label>
          <Input v-model="documento" />
        </div>
      </div>
      <div class="flex justify-end gap-2">
        <Button variant="outline" @click="close">{{ t('common.cancel') }}</Button>
        <Button :disabled="saving || !numPedido || !documento" @click="register">
          <Loader2 v-if="saving" class="h-4 w-4 mr-2 animate-spin" />
          {{ isNewAfterResolved() ? t('xprocessLink.registerNew') : t('xprocessLink.register') }}
        </Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
