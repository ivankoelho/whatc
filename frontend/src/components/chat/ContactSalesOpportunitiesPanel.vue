<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
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
  <div id="sales-opportunities-panel" class="w-80 border-l border-white/[0.08] light:border-gray-200 bg-[#111113] light:bg-white flex flex-col h-full min-h-0">
    <div class="flex items-center justify-between px-4 py-3 border-b border-white/[0.08] light:border-gray-200 shrink-0">
      <h3 class="text-sm font-medium text-white light:text-gray-900">{{ t('chat.salesOpportunities') }}</h3>
      <Button v-if="canCreate" variant="ghost" size="sm" @click="createDialogOpen = true">
        <Plus class="h-4 w-4 mr-1" />
        {{ t('sales.createOpportunity') }}
      </Button>
    </div>

    <ScrollArea orientation="vertical" class="flex-1 min-h-0">
      <div class="p-4 space-y-2">
        <RouterLink
          v-for="opp in opportunities"
          :key="opp.id"
          to="/sales/operation"
          class="block p-3 rounded-md border border-white/[0.08] light:border-gray-200 hover:bg-white/[0.04] light:hover:bg-gray-50 transition-colors"
        >
          <div class="flex items-center justify-between gap-2">
            <span class="font-mono text-xs text-white/50 light:text-muted-foreground">{{ opp.opportunity_number }}</span>
            <Badge :variant="statusVariant(opp)" class="shrink-0 text-xs">{{ statusLabel(opp) }}</Badge>
          </div>
          <p class="text-sm mt-1 text-white light:text-gray-900">{{ formatCurrency(opp.estimated_value) }}</p>
          <p v-if="opp.interest" class="text-xs mt-0.5 truncate text-white/50 light:text-muted-foreground">{{ opp.interest }}</p>
          <p v-if="conversionSourceLabel(opp)" class="text-xs mt-0.5 text-white/40 light:text-muted-foreground">{{ conversionSourceLabel(opp) }}</p>
        </RouterLink>

        <p v-if="!loading && opportunities.length === 0" class="text-sm text-white/40 light:text-muted-foreground text-center py-6">
          {{ t('chat.noSalesOpportunities') }}
        </p>
      </div>
    </ScrollArea>

    <CreateSalesOpportunityDialog
      v-model:open="createDialogOpen"
      :contact-id="contactId"
      @created="onCreated"
    />
  </div>
</template>
