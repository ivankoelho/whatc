<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Plus } from 'lucide-vue-next'
import { salesOpportunitiesService, type SalesOpportunity, type SalesOpportunityStage } from '@/services/api'
import { formatCurrency } from '@/lib/currency'
import { useAuthStore } from '@/stores/auth'
import CreateSalesOpportunityDialog from '@/components/sales/CreateSalesOpportunityDialog.vue'

const props = defineProps<{ contactId: string }>()

const { t } = useI18n()
const authStore = useAuthStore()
const canCreate = authStore.hasPermission('sales_opportunities', 'write')
const opportunities = ref<SalesOpportunity[]>([])
const loading = ref(false)
const createDialogOpen = ref(false)

function onCreated(opp: SalesOpportunity) {
  opportunities.value = [opp, ...opportunities.value.filter(o => o.id !== opp.id)]
}

const STAGE_LABEL_KEY: Record<SalesOpportunityStage, string> = {
  potencial: 'sales.stagePotencial',
  abrir_orcamento: 'sales.stageAbrirOrcamento',
  direcionada: 'sales.stageDirecionada',
}

// Aberta shows the funnel stage (more useful mid-flight than a bare
// "Aberta"); a closed opportunity shows its final outcome instead — same
// split the Kanban board itself uses (column = stage, badge = outcome).
function statusLabel(opp: SalesOpportunity): string {
  if (opp.status === 'aberta') return t(STAGE_LABEL_KEY[opp.stage])
  if (opp.status === 'convertida') return t('sales.statusConvertida')
  if (opp.status === 'perdida') return t('sales.statusPerdida')
  return t('sales.statusCancelada')
}

function statusVariant(opp: SalesOpportunity): 'default' | 'destructive' | 'outline' {
  if (opp.status === 'convertida') return 'default'
  if (opp.status === 'perdida' || opp.status === 'cancelada') return 'destructive'
  return 'outline'
}

// Only meaningful once converted — shows whether the sale is confirmed by
// the X2 reconciliation job or just the agent's own record, so nobody reads
// a manual "Convertida" as "confirmed by the ERP" (spec item F).
function conversionSourceLabel(opp: SalesOpportunity): string | null {
  if (opp.status !== 'convertida' || !opp.conversion_source) return null
  return opp.conversion_source === 'xprocess' ? t('sales.conversionSourceXProcess') : t('sales.conversionSourceManual')
}

async function load() {
  if (!props.contactId) return
  loading.value = true
  try {
    const res = await salesOpportunitiesService.list({ contact_id: props.contactId })
    opportunities.value = res.data.data.opportunities
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(() => props.contactId, load)

defineExpose({ refresh: load })
</script>

<template>
  <!-- Section inside ContactInfoPanel's own scrollable column (spec item 4)
       — same semantic tokens as its other sections, no own width/scroll. -->
  <div id="sales-opportunities-panel" class="border-t pt-4">
    <div class="flex items-center justify-between py-2">
      <h5 class="text-sm font-medium">{{ t('chat.salesOpportunities') }}</h5>
      <Button v-if="canCreate" variant="ghost" size="sm" class="h-7 px-2" @click="createDialogOpen = true">
        <Plus class="h-3.5 w-3.5 mr-1" />
        {{ t('sales.createOpportunity') }}
      </Button>
    </div>

    <div class="space-y-2">
      <RouterLink
        v-for="opp in opportunities"
        :key="opp.id"
        to="/sales/operation"
        class="block p-3 rounded-md border bg-muted/50 hover:bg-muted transition-colors"
      >
        <div class="flex items-center justify-between gap-2">
          <span class="font-mono text-xs text-muted-foreground">{{ opp.opportunity_number }}</span>
          <Badge :variant="statusVariant(opp)" class="shrink-0 text-xs">{{ statusLabel(opp) }}</Badge>
        </div>
        <p class="text-sm mt-1 font-semibold">{{ formatCurrency(opp.estimated_value) }}</p>
        <p v-if="opp.interest" class="text-xs mt-0.5 truncate text-muted-foreground">{{ opp.interest }}</p>
        <p v-if="conversionSourceLabel(opp)" class="text-xs mt-0.5 text-muted-foreground">{{ conversionSourceLabel(opp) }}</p>
      </RouterLink>

      <p v-if="!loading && opportunities.length === 0" class="text-sm text-muted-foreground text-center py-6">
        {{ t('chat.noSalesOpportunities') }}
      </p>
    </div>

    <CreateSalesOpportunityDialog
      v-model:open="createDialogOpen"
      :contact-id="contactId"
      @created="onCreated"
    />
  </div>
</template>
