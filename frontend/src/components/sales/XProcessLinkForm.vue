<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { salesOpportunityXProcessLinkService, type SalesOpportunityXProcessLink } from '@/services/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

const props = defineProps<{ opportunityId: string; editable: boolean }>()

const { t } = useI18n()
const link = ref<SalesOpportunityXProcessLink | null>(null)
const numPedido = ref('')
const documento = ref('')
const saving = ref(false)

async function load() {
  try {
    const res = await salesOpportunityXProcessLinkService.get(props.opportunityId)
    link.value = res.data.data
  } catch {
    link.value = null
  }
}

async function register() {
  if (!numPedido.value || !documento.value) return
  saving.value = true
  try {
    await salesOpportunityXProcessLinkService.upsert(props.opportunityId, {
      num_pedido: numPedido.value, documento: documento.value,
    })
    toast.success(t('xprocessLink.registered'))
    numPedido.value = ''
    documento.value = ''
    await load()
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-2 rounded-lg border p-3">
    <h4 class="text-sm font-medium">{{ t('xprocessLink.title') }}</h4>

    <div v-if="link" class="text-sm text-muted-foreground">
      <p>{{ t('xprocessLink.numPedido') }}: {{ link.num_pedido }}</p>
      <p v-if="link.status_xprocess">{{ t('xprocessLink.status') }}: {{ link.status_xprocess }}</p>
      <p v-if="link.pending_review" class="text-amber-600">{{ t('xprocessLink.pendingReview') }}</p>
    </div>

    <div v-if="editable && (!link || link.resolved_at)" class="flex flex-col gap-2 sm:flex-row">
      <div class="flex-1 space-y-1">
        <Label>{{ t('xprocessLink.numPedido') }}</Label>
        <Input v-model="numPedido" />
      </div>
      <div class="flex-1 space-y-1">
        <Label>{{ t('xprocessLink.documento') }}</Label>
        <Input v-model="documento" />
      </div>
      <Button class="self-end" :disabled="saving || !numPedido || !documento" @click="register">
        {{ t('xprocessLink.register') }}
      </Button>
    </div>
  </div>
</template>
