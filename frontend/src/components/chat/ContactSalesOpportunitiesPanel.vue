<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Plus, Sparkles } from 'lucide-vue-next'
import {
  salesOpportunitiesService,
  salesOpportunityXProcessCandidatesService,
  type SalesOpportunity,
  type SalesOpportunityStage,
  type SalesOpportunityXProcessCandidate,
} from '@/services/api'
import { formatCurrency } from '@/lib/currency'
import { formatQuantity } from '@/lib/salesUnits'
import { formatDate } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth'
import CreateSalesOpportunityDialog from '@/components/sales/CreateSalesOpportunityDialog.vue'
import XProcessLinkDialog from '@/components/sales/XProcessLinkDialog.vue'

const props = defineProps<{ contactId: string }>()

const { t } = useI18n()
const authStore = useAuthStore()
const canCreate = authStore.hasPermission('sales_opportunities', 'write')
const canLinkXProcess = authStore.hasPermission('sales_opportunities', 'write')
const opportunities = ref<SalesOpportunity[]>([])
const loading = ref(false)
const createDialogOpen = ref(false)

// Spec item G: "esse cliente comprou recentemente" — one candidate list per
// open opportunity, fetched automatically (no extra click) but only for
// opportunities that could plausibly have one (status aberta), since it's a
// live X2 call. Never auto-links anything.
const candidatesByOpportunity = ref<Record<string, SalesOpportunityXProcessCandidate[]>>({})
const linkDialogOpen = ref(false)
const linkDialogOpportunityId = ref('')
const linkDialogPrefill = ref<{ numPedido: string; documento: string } | null>(null)

async function loadCandidates(opp: SalesOpportunity) {
  if (opp.status !== 'aberta') return
  try {
    const res = await salesOpportunityXProcessCandidatesService.list(opp.id)
    candidatesByOpportunity.value[opp.id] = res.data.data.candidates
  } catch {
    // Best-effort hint — a failure here must never break the panel.
    candidatesByOpportunity.value[opp.id] = []
  }
}

function openLinkDialog(opp: SalesOpportunity, candidate: SalesOpportunityXProcessCandidate) {
  linkDialogOpportunityId.value = opp.id
  linkDialogPrefill.value = { numPedido: candidate.num_pedido, documento: opp.contact?.cpf_cnpj ?? '' }
  linkDialogOpen.value = true
}

function onLinked() {
  candidatesByOpportunity.value[linkDialogOpportunityId.value] = []
  load()
}

function onCreated(opp: SalesOpportunity) {
  opportunities.value = [opp, ...opportunities.value.filter(o => o.id !== opp.id)]
  loadCandidates(opp)
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
    for (const opp of opportunities.value) loadCandidates(opp)
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
        <p v-if="opp.estimated_quantity != null" class="text-xs mt-0.5 text-muted-foreground">
          {{ formatQuantity(opp.estimated_quantity, opp.unit_of_measure) }}
        </p>
        <p v-if="opp.status === 'convertida' && (opp.realized_value != null || opp.realized_quantity != null)" class="text-xs mt-0.5 text-muted-foreground">
          {{ t('sales.realizedLabel') }}: {{ formatCurrency(opp.realized_value) }}<span v-if="opp.realized_quantity != null"> · {{ formatQuantity(opp.realized_quantity, opp.unit_of_measure) }}</span>
        </p>
        <p v-if="opp.interest" class="text-xs mt-0.5 truncate text-muted-foreground">{{ opp.interest }}</p>
        <p v-if="conversionSourceLabel(opp)" class="text-xs mt-0.5 text-muted-foreground">{{ conversionSourceLabel(opp) }}</p>

        <!-- Spec item G: purchase found for this customer after the
             opportunity was opened, not yet linked to anything. Linking is
             always this explicit manual click — never automatic. -->
        <div
          v-for="candidate in candidatesByOpportunity[opp.id] || []"
          :key="candidate.num_pedido"
          class="mt-2 p-2 rounded-md border border-amber-500/30 bg-amber-500/10 text-xs"
          @click.stop.prevent
        >
          <p class="flex items-center gap-1 text-amber-600 dark:text-amber-400 font-medium">
            <Sparkles class="h-3 w-3 shrink-0" />
            {{ t('sales.xprocessCandidateHint', { date: formatDate(candidate.data_venda) }) }}
          </p>
          <p class="text-muted-foreground mt-0.5">{{ t('xprocessLink.numPedido') }} {{ candidate.num_pedido }} · {{ formatCurrency(candidate.valor_vendido) }}</p>
          <Button v-if="canLinkXProcess" variant="outline" size="sm" class="h-6 px-2 mt-1 text-xs" @click="openLinkDialog(opp, candidate)">
            {{ t('sales.xprocessCandidateLink') }}
          </Button>
        </div>
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

    <XProcessLinkDialog
      v-model:open="linkDialogOpen"
      :opportunity-id="linkDialogOpportunityId"
      :link="null"
      :prefill="linkDialogPrefill"
      @registered="onLinked"
    />
  </div>
</template>
