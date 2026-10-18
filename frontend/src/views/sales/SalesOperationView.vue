<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Card } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Skeleton } from '@/components/ui/skeleton'
import { PageHeader, DataTable, ErrorState, DeleteConfirmDialog, DateRangePicker, type Column } from '@/components/shared'
import { TrendingUp, Briefcase, Target, CheckCircle2, XCircle, Percent, AlertTriangle, Trash2, Pencil, Columns3, List, Store, UserRound, X, LayoutDashboard } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { useDateRange } from '@/composables/useDateRange'
import { salesOpportunitiesService } from '@/services/api'
import type { SalesOpportunity, SalesScopeInfo, SalesSummary } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDate } from '@/lib/utils'
import { formatCurrency } from '@/lib/currency'
import { formatQuantity } from '@/lib/salesUnits'
import { Doughnut } from '@/lib/charts'
import SalesOpportunityBoard from '@/components/sales/SalesOpportunityBoard.vue'
import LoseSalesOpportunityDialog from '@/components/sales/LoseSalesOpportunityDialog.vue'
import SalesOpportunityDetailsDialog from '@/components/sales/SalesOpportunityDetailsDialog.vue'

const { t } = useI18n()
const authStore = useAuthStore()

// Painel | Quadro | Lista. The Kanban is the day-to-day view, so it opens
// first; ?tab= keeps the choice shareable and survives a reload.
type CrmTab = 'dashboard' | 'board' | 'list'
const CRM_TABS: CrmTab[] = ['dashboard', 'board', 'list']
const route = useRoute()
const router = useRouter()
const activeTab = computed<CrmTab>({
  get: () => (CRM_TABS.includes(route.query.tab as CrmTab) ? (route.query.tab as CrmTab) : 'board'),
  set: (tab) => { router.replace({ query: { ...route.query, tab } }) },
})
const boardRef = ref<InstanceType<typeof SalesOpportunityBoard> | null>(null)

// One sales screen for everyone (it replaced "Minha Operação" + "Dashboard de
// Vendas"). The backend scopes every request to what the user may see —
// admin/SAC supervisor: all; store supervisor (view_team): their teams'
// sellers; seller: their own — and /scope says which sellers/stores the
// manual filters below may offer.
const scopeInfo = ref<SalesScopeInfo>({ scope: 'own', sellers: [], units: [] })
const unitFilter = ref('all')
const sellerFilter = ref('all')
const filterParams = computed(() => {
  const p: { assigned_user_id?: string; unit_id?: string } = {}
  if (sellerFilter.value !== 'all') p.assigned_user_id = sellerFilter.value
  if (unitFilter.value !== 'all') p.unit_id = unitFilter.value
  return p
})
const hasFilters = computed(() => unitFilter.value !== 'all' || sellerFilter.value !== 'all')
function clearFilters() {
  unitFilter.value = 'all'
  sellerFilter.value = 'all'
}

async function fetchScope() {
  try {
    const { data } = await salesOpportunitiesService.scope()
    scopeInfo.value = data.data
  } catch {
    // Without it the screen still works, just without the manual filters.
  }
}

// Indicator period (closings inside it; the open pipeline is always "now").
const {
  selectedRange,
  customDateRange,
  isDatePickerOpen,
  dateRange,
  formatDateRangeDisplay,
  applyCustomRange: applyCustomRangeBase,
} = useDateRange({ storageKey: 'sales_operation' })

const summary = ref<SalesSummary | null>(null)
async function fetchSummary() {
  try {
    const { from, to } = dateRange.value
    const { data } = await salesOpportunitiesService.summary({ ...filterParams.value, from, to })
    summary.value = data.data
  } catch (e) {
    toast.error(getErrorMessage(e, t('sales.walletLoadFailed')))
  }
}
function applyCustomRange() {
  applyCustomRangeBase()
  fetchSummary()
}
watch(selectedRange, (v) => { if (v !== 'custom') fetchSummary() })

// ponytail: the list tab still loads one page of up to 100 (API cap) and
// filters by stage client-side; paginate server-side if a scope outgrows it.
const wallet = ref<SalesOpportunity[]>([])
const walletLoading = ref(false)
const walletFailed = ref(false)

// Every mutation path below calls this: list + indicators stay in step.
async function fetchWallet() {
  walletLoading.value = true
  walletFailed.value = false
  fetchSummary()
  try {
    const { data } = await salesOpportunitiesService.list({ ...filterParams.value, limit: '100' })
    wallet.value = data.data.opportunities
  } catch (e) {
    walletFailed.value = true
    toast.error(getErrorMessage(e, t('sales.walletLoadFailed')))
  } finally {
    walletLoading.value = false
  }
}
watch(filterParams, fetchWallet)

const stats = computed(() => {
  const s = summary.value
  return {
    total: s?.total ?? 0,
    potencial: s?.potencial ?? 0,
    convertida: s?.convertida ?? 0,
    perdida: s?.perdida ?? 0,
    semDirecionamento: s?.sem_direcionamento ?? 0,
    conversionRate: s?.conversion_rate ?? 0,
    valorProspecto: s?.valor_prospecto ?? 0,
    valorConvertido: s?.valor_convertido ?? 0,
    valorPerdido: s?.valor_perdido ?? 0,
  }
})
const statsLoading = computed(() => summary.value === null)

// Painel KPI tiles. Tones are semantic (sky = volume, amber = early stage,
// emerald = won, rose = lost, teal = rate, orange = needs action).
const kpis = computed(() => [
  { key: 'total', label: t('sales.statAttendances'), value: String(stats.value.total), icon: Briefcase, tone: 'bg-sky-500/15 text-sky-600 dark:text-sky-300' },
  { key: 'potencial', label: t('sales.statPotential'), value: String(stats.value.potencial), icon: Target, tone: 'bg-amber-500/15 text-amber-600 dark:text-amber-300' },
  { key: 'convertida', label: t('sales.statConverted'), value: String(stats.value.convertida), icon: CheckCircle2, tone: 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-300' },
  { key: 'perdida', label: t('sales.statLost'), value: String(stats.value.perdida), icon: XCircle, tone: 'bg-rose-500/15 text-rose-600 dark:text-rose-300' },
  { key: 'conversion', label: t('sales.statConversion'), value: `${stats.value.conversionRate.toFixed(1)}%`, icon: Percent, tone: 'bg-teal-500/15 text-teal-600 dark:text-teal-300' },
  { key: 'semDirecionamento', label: t('sales.statNoDirecionamento'), value: String(stats.value.semDirecionamento), icon: AlertTriangle, tone: 'bg-orange-500/15 text-orange-600 dark:text-orange-300' },
])

// Value distribution: 3 slices, so a donut fits; the legend beside it is a
// table with value and share (the accessible view, and no need to hover).
// Sky/emerald/rose rather than red-vs-green alone.
const valueSlices = computed(() => {
  const rows = [
    { key: 'prospect', label: t('sales.statValueProspect'), value: stats.value.valorProspecto, color: '#0ea5e9' },
    { key: 'converted', label: t('sales.statValueConverted'), value: stats.value.valorConvertido, color: '#10b981' },
    { key: 'lost', label: t('sales.statValueLost'), value: stats.value.valorPerdido, color: '#f43f5e' },
  ]
  const total = rows.reduce((sum, r) => sum + r.value, 0)
  return { total, rows: rows.map(r => ({ ...r, share: total > 0 ? (r.value / total) * 100 : 0 })) }
})
const valueChartSummary = computed(() =>
  valueSlices.value.rows.map(r => `${r.label}: ${formatCurrency(r.value)} (${r.share.toFixed(0)}%)`).join('; '))

const valueChartData = computed(() => ({
  labels: valueSlices.value.rows.map(r => r.label),
  datasets: [{
    data: valueSlices.value.rows.map(r => r.value),
    backgroundColor: valueSlices.value.rows.map(r => r.color),
    borderWidth: 2,
    borderColor: 'transparent',
  }],
}))

const valueChartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  cutout: '68%',
  // The global Chart.js defaults set in lib/charts.ts (mode: 'index',
  // intersect: false) are meant for line/bar charts, where "index" means
  // an x-axis category. On a Doughnut those defaults make Chart.js treat
  // every slice sharing the same data-array index as "hovered" together —
  // hovering the Convertido arc could surface Perdido's tooltip too (even
  // showing R$ 0,00) since they're neighboring indexes in the same
  // dataset. A pie/doughnut needs point-level hit testing instead: only
  // the slice the cursor is actually over.
  interaction: { mode: 'nearest' as const, intersect: true },
  plugins: {
    legend: { display: false }, // the table beside the chart is the legend
    tooltip: { callbacks: { label: (ctx: { label: string; raw: unknown }) => `${ctx.label}: ${formatCurrency(ctx.raw as number)}` } },
  },
}

// This tab used to only show convertida/perdida. Now shows every phase —
// wallet already fetches every status, the filter just narrows the view —
// with a filter selector, so it doubles as the one place to browse the
// whole carteira instead of only closed deals.
//
// The filter is keyed by stage for open opportunities and by status for
// closed ones (not just status=aberta/convertida/perdida/cancelada) —
// "Aberta" alone used to lump Potencial+Abrir orçamento+Direcionada
// together, so selecting/deleting just one stage meant doing it one row at
// a time. Matches the Kanban's own 5 columns + a "Todos" catch-all.
const isSuperAdmin = computed(() => authStore.user?.is_super_admin ?? false)
const FILTER_KEYS = ['all', 'potencial', 'abrir_orcamento', 'direcionada', 'convertida', 'perdida', 'cancelada'] as const
type FilterKey = typeof FILTER_KEYS[number]
const FILTER_LABEL_KEY: Record<FilterKey, string> = {
  all: 'sales.statusFilterAll',
  potencial: 'sales.stagePotencial',
  abrir_orcamento: 'sales.stageAbrirOrcamento',
  direcionada: 'sales.stageDirecionada',
  convertida: 'sales.statusConvertida',
  perdida: 'sales.statusPerdida',
  cancelada: 'sales.statusCancelada',
}
// Status-only labels for the table's own Status badge column — distinct
// from FILTER_LABEL_KEY, which breaks "aberta" down by stage instead.
const STATUS_BADGE_LABEL_KEY: Record<SalesOpportunity['status'], string> = {
  aberta: 'sales.statusAberta',
  convertida: 'sales.statusConvertida',
  perdida: 'sales.statusPerdida',
  cancelada: 'sales.statusCancelada',
}
const opportunityFilter = ref<FilterKey>('all')

function matchesFilter(o: SalesOpportunity, key: FilterKey): boolean {
  if (key === 'all') return true
  if (key === 'convertida' || key === 'perdida' || key === 'cancelada') return o.status === key
  return o.status === 'aberta' && o.stage === key
}

const filteredOpportunities = computed(() => wallet.value.filter(o => matchesFilter(o, opportunityFilter.value)))

const allColumns = computed<Column<SalesOpportunity>[]>(() => {
  const cols: Column<SalesOpportunity>[] = []
  if (isSuperAdmin.value) cols.push({ key: 'select', label: '', width: 'w-10' })
  cols.push(
    { key: 'opportunity_number', label: t('sales.columnOpportunityNumber') },
    { key: 'contact', label: t('sales.columnContact') },
    { key: 'stage', label: t('sales.columnStage') },
    { key: 'estimated_value', label: t('sales.columnEstimatedValue') },
    { key: 'quantity', label: t('sales.columnQuantity') },
    { key: 'realized', label: t('sales.columnRealized') },
    { key: 'status', label: t('sales.columnStatus') },
    { key: 'conversion_source', label: t('sales.columnConversionSource') },
    { key: 'closed_at', label: t('sales.columnClosedDate') },
    { key: 'loss_reason', label: t('sales.columnLossReason') },
    ...(authStore.hasPermission('sales_opportunities', 'write') ? [{ key: 'actions', label: '', width: 'w-10' }] : []),
  )
  return cols
})

const STAGE_LABEL_KEY: Record<string, string> = {
  potencial: 'sales.stagePotencial',
  abrir_orcamento: 'sales.stageAbrirOrcamento',
  direcionada: 'sales.stageDirecionada',
}

// Bulk delete (super admin only, mirrors the backend's own gate) — checkbox
// selection lives here instead of on each Kanban card, so the card stays
// visually clean and this list is the one deliberate "admin" surface.
const selectedIds = ref<Set<string>>(new Set())
const allSelected = computed(() =>
  filteredOpportunities.value.length > 0 && filteredOpportunities.value.every(o => selectedIds.value.has(o.id))
)
function toggleSelectAll() {
  selectedIds.value = allSelected.value ? new Set() : new Set(filteredOpportunities.value.map(o => o.id))
}
function toggleSelect(id: string) {
  const next = new Set(selectedIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selectedIds.value = next
}
watch(opportunityFilter, () => { selectedIds.value = new Set() })

const bulkDeleteDialogOpen = ref(false)
const bulkDeleting = ref(false)

async function confirmBulkDelete() {
  bulkDeleting.value = true
  const ids = Array.from(selectedIds.value)
  try {
    await Promise.all(ids.map(id => salesOpportunitiesService.delete(id)))
    toast.success(t('sales.opportunitiesDeleted', { count: ids.length }))
    selectedIds.value = new Set()
  } catch (e) {
    toast.error(getErrorMessage(e, t('sales.opportunityDeleteFailed')))
  } finally {
    bulkDeleting.value = false
    bulkDeleteDialogOpen.value = false
    fetchWallet()
    boardRef.value?.refresh()
  }
}

const LOSS_REASON_LABEL_KEY: Record<string, string> = {
  cliente_desistiu: 'sales.lossReasonClienteDesistiu',
  preco: 'sales.lossReasonPreco',
  prazo: 'sales.lossReasonPrazo',
  indisponibilidade: 'sales.lossReasonIndisponibilidade',
  comprou_concorrente: 'sales.lossReasonComprouConcorrente',
  sem_retorno: 'sales.lossReasonSemRetorno',
  problema_comercial: 'sales.lossReasonProblemaComercial',
  outro: 'sales.lossReasonOutro',
}

async function handleConvert(opportunity: SalesOpportunity) {
  try {
    await salesOpportunitiesService.convert(opportunity.id)
    toast.success(t('sales.conversionRecorded'))
    boardRef.value?.refresh()
    fetchWallet()
  } catch (e) {
    toast.error(getErrorMessage(e, t('sales.conversionFailed')))
  }
}

// Quantity/realized editing: an open opportunity takes the estimates, a converted one
// the realized values (the dialog decides by status); lost/cancelled are read-only.
const detailsOpen = ref(false)
const detailsTarget = ref<SalesOpportunity | null>(null)
function openDetails(opportunity: SalesOpportunity) {
  detailsTarget.value = opportunity
  detailsOpen.value = true
}
function handleDetailsSaved() {
  boardRef.value?.refresh()
  fetchWallet()
}

const loseDialogOpen = ref(false)
const loseTarget = ref<SalesOpportunity | null>(null)

function handleLose(opportunity: SalesOpportunity) {
  loseTarget.value = opportunity
  loseDialogOpen.value = true
}

function handleLost() {
  boardRef.value?.refresh()
  fetchWallet()
}

function handleStageChange() {
  fetchWallet()
}

function handleDirecionamentoChange() {
  fetchWallet()
}

function handleDetailsChange() {
  fetchWallet()
}

onMounted(() => {
  fetchScope()
  fetchWallet()
})
</script>

<template>
  <div class="flex h-full bg-[#0a0a0b] light:bg-gray-50">
    <Tabs v-model="activeTab" orientation="vertical" class="flex min-h-0 min-w-0 flex-1">
      <TabsList class="page-rail">
        <TabsTrigger value="dashboard" :title="$t('sales.tabDashboard')" class="page-rail-item"><LayoutDashboard class="h-5 w-5" aria-hidden="true" /><span class="page-rail-label">{{ $t('sales.tabDashboard') }}</span></TabsTrigger>
        <TabsTrigger value="board" :title="$t('sales.tabBoard')" class="page-rail-item"><Columns3 class="h-5 w-5" aria-hidden="true" /><span class="page-rail-label">{{ $t('sales.tabBoard') }}</span></TabsTrigger>
        <TabsTrigger value="list" :title="$t('sales.tabList')" class="page-rail-item"><List class="h-5 w-5" aria-hidden="true" /><span class="page-rail-label">{{ $t('sales.tabList') }}</span></TabsTrigger>
      </TabsList>

      <div class="flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader :title="$t('sales.title')" :description="$t('sales.scope_' + scopeInfo.scope)" :icon="TrendingUp" icon-gradient="bg-gradient-to-br from-emerald-500 to-teal-600 shadow-emerald-500/20">
        <!-- Filters and period sit next to the title, like Protocolos.
             Store/seller only for supervisors/admins (sellers only see their own). -->
        <template #actions>
          <div class="flex flex-wrap items-center justify-end gap-2" role="group" :aria-label="$t('sales.filtersLabel')">
        <template v-if="scopeInfo.scope !== 'own'">
        <Select v-if="scopeInfo.units.length" v-model="unitFilter">
          <SelectTrigger class="h-9 w-44 text-[13px]" :aria-label="$t('sales.filterStore')">
            <Store class="mr-1.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{{ $t('sales.allStores') }}</SelectItem>
            <SelectItem v-for="u in scopeInfo.units" :key="u.id" :value="u.id">{{ u.name }}</SelectItem>
          </SelectContent>
        </Select>
        <Select v-model="sellerFilter">
          <SelectTrigger class="h-9 w-52 text-[13px]" :aria-label="$t('sales.filterSeller')">
            <UserRound class="mr-1.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{{ $t('sales.allSellers') }}</SelectItem>
            <SelectItem v-for="s in scopeInfo.sellers" :key="s.id" :value="s.id">{{ s.name }}</SelectItem>
          </SelectContent>
        </Select>
        <Button v-if="hasFilters" variant="ghost" size="sm" class="h-9 text-[13px]" @click="clearFilters">
          <X class="mr-1 h-3.5 w-3.5" aria-hidden="true" />{{ $t('common.clear') }}
        </Button>
        </template>
          <!-- The period only drives the Painel's numbers. -->
          <DateRangePicker
            v-if="activeTab === 'dashboard'"
            v-model:selected-range="selectedRange"
            v-model:custom-date-range="customDateRange"
            v-model:is-date-picker-open="isDatePickerOpen"
            :format-date-range-display="formatDateRangeDisplay"
            @apply-custom="applyCustomRange"
          />
          </div>
        </template>
      </PageHeader>


      <!-- Painel: indicators and value distribution only -->
      <TabsContent value="dashboard" class="mt-0 min-h-0 flex-1">
        <ScrollArea orientation="vertical" class="h-full">
          <div class="space-y-6 p-6">
            <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-6">
              <template v-if="statsLoading">
                <div v-for="i in 6" :key="i" class="rounded-2xl bg-card p-5 shadow-sm ring-1 ring-black/[0.05] dark:ring-white/[0.07]">
                  <Skeleton class="mb-3 h-4 w-24" />
                  <Skeleton class="h-8 w-16" />
                </div>
              </template>
              <template v-else>
                <div
                  v-for="k in kpis"
                  :key="k.key"
                  class="rounded-2xl bg-card p-5 shadow-sm ring-1 ring-black/[0.05] dark:ring-white/[0.07]"
                >
                  <div class="flex items-start justify-between gap-3">
                    <span class="text-[13px] font-medium leading-snug text-muted-foreground">{{ k.label }}</span>
                    <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl" :class="k.tone">
                      <component :is="k.icon" class="h-[18px] w-[18px]" aria-hidden="true" />
                    </span>
                  </div>
                  <div class="mt-3 text-[28px] font-bold leading-none tabular-nums text-foreground">{{ k.value }}</div>
                </div>
              </template>
            </div>

            <section class="rounded-2xl bg-card p-6 shadow-sm ring-1 ring-black/[0.05] dark:ring-white/[0.07]" aria-labelledby="crm-value-title">
              <div class="flex flex-wrap items-baseline justify-between gap-2">
                <h3 id="crm-value-title" class="text-[15px] font-semibold text-foreground">{{ $t('sales.valueChartTitle') }}</h3>
                <span class="text-sm text-muted-foreground">{{ $t('sales.valueTotal') }}: <span class="font-semibold tabular-nums text-foreground">{{ formatCurrency(valueSlices.total) }}</span></span>
              </div>
              <div v-if="statsLoading" class="mt-6 grid items-center gap-8 md:grid-cols-[220px_minmax(0,1fr)]">
                <Skeleton class="mx-auto h-48 w-48 rounded-full" />
                <div class="space-y-3"><Skeleton v-for="i in 3" :key="i" class="h-10 w-full" /></div>
              </div>
              <p v-else-if="valueSlices.total === 0" class="mt-6 rounded-xl bg-muted/50 px-4 py-10 text-center text-sm text-muted-foreground">
                {{ $t('sales.valueEmpty') }}
              </p>
              <div v-else class="mt-6 grid items-center gap-8 md:grid-cols-[220px_minmax(0,1fr)]">
                <div class="mx-auto h-52 w-52" role="img" :aria-label="valueChartSummary">
                  <Doughnut :data="valueChartData" :options="valueChartOptions" />
                </div>
                <table class="w-full text-sm">
                  <caption class="sr-only">{{ $t('sales.valueChartTitle') }}</caption>
                  <thead class="sr-only"><tr><th>{{ $t('sales.valueColStatus') }}</th><th>{{ $t('sales.valueColValue') }}</th><th>{{ $t('sales.valueColShare') }}</th></tr></thead>
                  <tbody>
                    <tr v-for="r in valueSlices.rows" :key="r.key" class="border-b last:border-0">
                      <th scope="row" class="py-3 pr-4 text-left font-medium text-foreground">
                        <span class="inline-flex items-center gap-2.5">
                          <span class="h-3 w-3 shrink-0 rounded-full" :style="{ backgroundColor: r.color }" aria-hidden="true" />
                          {{ r.label }}
                        </span>
                      </th>
                      <td class="py-3 pr-4 text-right font-semibold tabular-nums text-foreground">{{ formatCurrency(r.value) }}</td>
                      <td class="w-16 py-3 text-right tabular-nums text-muted-foreground">{{ r.share.toFixed(0) }}%</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </section>
          </div>
        </ScrollArea>
      </TabsContent>

      <!-- Quadro: the Kanban takes the whole area, like Protocolos -->
      <TabsContent value="board" class="mt-0 min-h-0 flex-1">
        <div class="h-full">
          <SalesOpportunityBoard
            ref="boardRef"
            :filters="filterParams"
            @convert="handleConvert"
            @lose="handleLose"
            @stage-change="handleStageChange"
            @direcionamento-change="handleDirecionamentoChange"
            @details-change="handleDetailsChange"
          />
        </div>
      </TabsContent>

      <!-- Lista: the full table -->
      <TabsContent value="list" class="mt-0 min-h-0 flex-1">
        <ErrorState
          v-if="walletFailed"
          :title="$t('common.loadErrorTitle')"
          :description="$t('common.loadErrorDescription')"
          :retry-label="$t('common.retryLoad')"
          class="h-full"
          @retry="fetchWallet"
        />
        <ScrollArea v-else orientation="vertical" class="flex-1 h-full">
          <div class="p-6 space-y-4">
            <!-- One row, aligned to the same left edge as the table below —
                 filter and bulk-selection controls used to be two separate
                 rows (filter right-aligned, select-all left-aligned),
                 misaligned with each other and with the Card/table. -->
            <div class="flex items-center justify-between gap-3 flex-wrap">
              <label v-if="isSuperAdmin" class="flex items-center gap-2 text-sm cursor-pointer select-none">
                <Checkbox :checked="allSelected" @update:checked="toggleSelectAll" />
                {{ $t('sales.selectAll') }}
              </label>
              <div v-else />

              <div class="flex items-center gap-3">
                <Button
                  v-if="isSuperAdmin && selectedIds.size > 0"
                  variant="destructive"
                  size="sm"
                  @click="bulkDeleteDialogOpen = true"
                >
                  <Trash2 class="h-4 w-4 mr-2" />
                  {{ $t('sales.deleteSelected', { count: selectedIds.size }) }}
                </Button>
                <Select v-model="opportunityFilter">
                  <SelectTrigger class="w-48">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem v-for="k in FILTER_KEYS" :key="k" :value="k">
                      {{ $t(FILTER_LABEL_KEY[k]) }}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>

            <Card>
              <DataTable
                :items="filteredOpportunities"
                :columns="allColumns"
                :is-loading="walletLoading"
                :empty-icon="TrendingUp"
                :empty-title="$t('sales.noOpportunitiesFound')"
                item-name="opportunities"
              >
                <template #cell-select="{ item }">
                  <Checkbox :checked="selectedIds.has(item.id)" @update:checked="toggleSelect(item.id)" />
                </template>
                <template #cell-contact="{ item }">
                  {{ item.contact?.profile_name || item.contact_id }}
                </template>
                <template #cell-stage="{ item }">
                  {{ $t(STAGE_LABEL_KEY[item.stage]) }}
                </template>
                <template #cell-estimated_value="{ item }">
                  {{ formatCurrency(item.estimated_value) }}
                </template>
                <template #cell-quantity="{ item }">
                  {{ formatQuantity(item.estimated_quantity, item.unit_of_measure) }}
                </template>
                <template #cell-realized="{ item }">
                  <span v-if="item.status === 'convertida'">
                    {{ formatCurrency(item.realized_value) }}<span v-if="item.realized_quantity != null"> · {{ formatQuantity(item.realized_quantity, item.unit_of_measure) }}</span>
                  </span>
                  <span v-else>—</span>
                </template>
                <template #cell-actions="{ item }">
                  <Button
                    v-if="item.status === 'aberta' || item.status === 'convertida'"
                    data-testid="sales-wallet-edit-details"
                    variant="ghost"
                    size="icon"
                    class="h-7 w-7"
                    :title="$t('sales.editDetails')"
                    @click="openDetails(item)"
                  >
                    <Pencil class="h-3.5 w-3.5" />
                  </Button>
                </template>
                <template #cell-status="{ item }">
                  <Badge :variant="item.status === 'convertida' ? 'success' : item.status === 'aberta' ? 'outline' : 'destructive'">
                    {{ $t(STATUS_BADGE_LABEL_KEY[item.status]) }}
                  </Badge>
                </template>
                <template #cell-conversion_source="{ item }">
                  <span v-if="item.status === 'convertida' && item.conversion_source">
                    {{ item.conversion_source === 'xprocess' ? $t('sales.conversionSourceXProcess') : $t('sales.conversionSourceManual') }}
                  </span>
                  <span v-else>—</span>
                </template>
                <template #cell-closed_at="{ item }">
                  {{ item.status === 'aberta' ? '—' : formatDate(item.converted_at || item.lost_at || item.cancelled_at || item.stage_changed_at) }}
                </template>
                <template #cell-loss_reason="{ item }">
                  {{ item.loss_reason ? $t(LOSS_REASON_LABEL_KEY[item.loss_reason]) : '—' }}
                </template>
              </DataTable>
            </Card>
          </div>
        </ScrollArea>
      </TabsContent>
      </div>
    </Tabs>

    <SalesOpportunityDetailsDialog
      v-if="detailsTarget"
      v-model:open="detailsOpen"
      :opportunity="detailsTarget"
      @saved="handleDetailsSaved"
    />

    <LoseSalesOpportunityDialog
      v-model:open="loseDialogOpen"
      :opportunity="loseTarget"
      @lost="handleLost"
    />

    <DeleteConfirmDialog
      v-model:open="bulkDeleteDialogOpen"
      :title="$t('sales.deleteSelectedTitle')"
      :is-submitting="bulkDeleting"
      @confirm="confirmBulkDelete"
    >
      <template #description>
        <p class="text-sm text-muted-foreground">{{ $t('sales.deleteSelectedWarning', { count: selectedIds.size }) }}</p>
      </template>
    </DeleteConfirmDialog>
  </div>
</template>
