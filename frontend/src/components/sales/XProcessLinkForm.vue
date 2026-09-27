<script setup lang="ts">
import { ref } from 'vue'
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

// Collapsed by default: a board can render up to 100 cards at once, and this
// section's GET is only useful once the user actually looks at it. `loaded`
// makes sure toggling open/closed repeatedly doesn't refetch every time.
const expanded = ref(false)
const loading = ref(false)
const loaded = ref(false)

async function load() {
  loading.value = true
  try {
    const res = await salesOpportunityXProcessLinkService.get(props.opportunityId)
    link.value = res.data.data
    // An open link (resolved_at nulo) is editable in place (design §6) — show
    // the agent what's already registered instead of an empty form. A
    // resolved link is immutable, so registering again is a NEW vínculo:
    // leave the fields blank rather than pre-filling historical data.
    if (link.value && !link.value.resolved_at) {
      numPedido.value = link.value.num_pedido ?? ''
      documento.value = link.value.documento ?? ''
    } else {
      numPedido.value = ''
      documento.value = ''
    }
  } catch {
    link.value = null
  } finally {
    loading.value = false
  }
}

function toggle() {
  expanded.value = !expanded.value
  if (expanded.value && !loaded.value) {
    loaded.value = true
    load()
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
  } catch {
    toast.error(t('xprocessLink.registerFailure'))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="space-y-2 rounded-lg border p-3">
    <button
      type="button"
      class="flex w-full items-center justify-between text-sm font-medium"
      :aria-expanded="expanded"
      @click="toggle"
    >
      <span>{{ t('xprocessLink.title') }}</span>
      <span class="text-xs text-muted-foreground">{{ expanded ? '▾' : '▸' }}</span>
    </button>

    <template v-if="expanded">
      <p v-if="loading" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>
      <template v-else>
        <div v-if="link" class="text-sm text-muted-foreground">
          <p>{{ t('xprocessLink.numPedido') }}: {{ link.num_pedido }}</p>
          <p v-if="link.status_xprocess">{{ t('xprocessLink.status') }}: {{ link.status_xprocess }}</p>
          <p v-if="link.pending_review" class="text-amber-600">{{ t('xprocessLink.pendingReview') }}</p>
        </div>

        <div v-if="editable" class="flex flex-col gap-2 sm:flex-row">
          <div class="flex-1 space-y-1">
            <Label>{{ link && link.resolved_at ? t('xprocessLink.numPedidoNew') : t('xprocessLink.numPedido') }}</Label>
            <Input v-model="numPedido" />
          </div>
          <div class="flex-1 space-y-1">
            <Label>{{ t('xprocessLink.documento') }}</Label>
            <Input v-model="documento" />
          </div>
          <Button class="self-end" :disabled="saving || !numPedido || !documento" @click="register">
            {{ link && link.resolved_at ? t('xprocessLink.registerNew') : t('xprocessLink.register') }}
          </Button>
        </div>
      </template>
    </template>
  </div>
</template>
