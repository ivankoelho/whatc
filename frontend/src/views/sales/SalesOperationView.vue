<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
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
import { PageHeader, DataTable, ErrorState, DeleteConfirmDialog, type Column } from '@/components/shared'
import { TrendingUp, Briefcase, Target, CheckCircle2, XCircle, Percent, AlertTriangle, Wallet, PiggyBank, TrendingDown, Trash2 } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { salesOpportunitiesService } from '@/services/api'
import type { SalesOpportunity } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDate } from '@/lib/utils'
import { formatCurrency } from '@/lib/currency'
import { Doughnut } from '@/lib/charts'
import SalesOpportunityBoard from '@/components/sales/SalesOpportunityBoard.vue'
import LoseSalesOpportunityDialog from '@/components/sales/LoseSalesOpportunityDialog.vue'

const { t } = useI18n()
const authStore = useAuthStore()
const currentUserId = computed(() => authStore.user?.id ?? '')

const activeTab = ref<'wallet' | 'closed'>('wallet')
const boardRef = ref<InstanceType<typeof SalesOpportunityBoard> | null>(null)

// ponytail: "Minha carteira" is client-aggregated for this first cut (spec
// §8/Task 15) — a single fetch of the current user's opportunities (all
// statuses, up to the 100-item API cap) drives both the stats row and the
// closed-sales tab, instead of a dedicated backend aggregation endpoint. A
// user with more than 100 opportunities gets undercounted stats. Upgrade
// path: Task 16's widget-engine numbers replace this entirely.
const wallet = ref<SalesOpportunity[]>([])
const walletLoading = ref(false)
const walletFailed = ref(false)

async function fetchWallet() {
  if (!currentUserId.value) return
  walletLoading.value = true
  walletFailed.value = false
  try {
    const { data } = await salesOpportunitiesService.list({ assigned_user_id: currentUserId.value, limit: '100' })
    wallet.value = data.data.opportunities
  } catch (e) {
    walletFailed.value = true
    toast.error(getErrorMessage(e, t('sales.walletLoadFailed')))
  } finally {
    walletLoading.value = false
  }
}

function sumEstimatedValue(opportunities: SalesOpportunity[]): number {
  return opportunities.reduce((sum, o) => sum + (o.estimated_value ?? 0), 0)
}

const stats = computed(() => {
  const total = wallet.value.length
  const potencial = wallet.value.filter(o => o.stage === 'potencial').length
  const convertida = wallet.value.filter(o => o.status === 'convertida').length
  const perdida = wallet.value.filter(o => o.status === 'perdida').length
  const semDirecionamento = wallet.value.filter(o => o.status === 'aberta' && !o.direcionamento).length
  const closedTotal = convertida + perdida
  const conversionRate = closedTotal > 0 ? (convertida / closedTotal) * 100 : 0
  // Value totals the agent needs alongside the counts above — how much is
  // still open (prospecção/pipeline) vs. how much already closed either way.
  const valorProspecto = sumEstimatedValue(wallet.value.filter(o => o.status === 'aberta'))
  const valorConvertido = sumEstimatedValue(wallet.value.filter(o => o.status === 'convertida'))
  const valorPerdido = sumEstimatedValue(wallet.value.filter(o => o.status === 'perdida'))
  return { total, potencial, convertida, perdida, semDirecionamento, conversionRate, valorProspecto, valorConvertido, valorPerdido }
})

const valueChartData = computed(() => ({
  labels: [t('sales.statValueProspect'), t('sales.statValueConverted'), t('sales.statValueLost')],
  datasets: [{
    data: [stats.value.valorProspecto, stats.value.valorConvertido, stats.value.valorPerdido],
    backgroundColor: ['#8b5cf6', '#10b981', '#ef4444'],
    borderWidth: 0,
  }],
}))

const valueChartOptions = {
  responsive: true,
  maintainAspectRatio: false,
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
    legend: { position: 'bottom' as const },
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
    { key: 'status', label: t('sales.columnStatus') },
    { key: 'conversion_source', label: t('sales.columnConversionSource') },
    { key: 'closed_at', label: t('sales.columnClosedDate') },
    { key: 'loss_reason', label: t('sales.columnLossReason') },
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

onMounted(fetchWallet)
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('sales.title')" :description="$t('sales.subtitle')" :icon="TrendingUp" icon-gradient="bg-gradient-to-br from-emerald-500 to-teal-600 shadow-emerald-500/20" />

    <Tabs v-model="activeTab" class="flex-1 min-h-0 flex flex-col">
      <div class="px-6 pt-4">
        <TabsList>
          <TabsTrigger value="wallet">{{ $t('sales.tabMyWallet') }}</TabsTrigger>
          <TabsTrigger value="closed">{{ $t('sales.tabAllOpportunities') }}</TabsTrigger>
        </TabsList>
      </div>

      <TabsContent value="wallet" class="flex-1 min-h-0">
        <ErrorState
          v-if="walletFailed"
          :title="$t('common.loadErrorTitle')"
          :description="$t('common.loadErrorDescription')"
          :retry-label="$t('common.retryLoad')"
          class="flex-1"
          @retry="fetchWallet"
        />
        <ScrollArea v-else orientation="vertical" class="flex-1 h-full">
          <div class="p-6 space-y-6">
            <div class="grid gap-4 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
              <template v-if="walletLoading">
                <div v-for="i in 6" :key="i" class="rounded-xl border border-white/[0.08] bg-white/[0.02] p-6 light:bg-white light:border-gray-200">
                  <Skeleton class="h-4 w-24 mb-3 bg-white/[0.08] light:bg-gray-200" />
                  <Skeleton class="h-8 w-16 bg-white/[0.08] light:bg-gray-200" />
                </div>
              </template>
              <template v-else>
                <div class="card-depth rounded-xl border border-white/[0.08] bg-white/[0.04] p-6 light:bg-white light:border-gray-200">
                  <div class="flex items-center justify-between pb-2">
                    <span class="text-sm font-medium text-white/50 light:text-gray-500">{{ $t('sales.statAttendances') }}</span>
                    <div class="h-10 w-10 rounded-lg bg-blue-500/20 flex items-center justify-center">
                      <Briefcase class="h-5 w-5 text-blue-400" />
                    </div>
                  </div>
                  <div class="text-3xl font-bold text-white light:text-gray-900">{{ stats.total }}</div>
                </div>
                <div class="card-depth rounded-xl border border-white/[0.08] bg-white/[0.04] p-6 light:bg-white light:border-gray-200">
                  <div class="flex items-center justify-between pb-2">
                    <span class="text-sm font-medium text-white/50 light:text-gray-500">{{ $t('sales.statPotential') }}</span>
                    <div class="h-10 w-10 rounded-lg bg-amber-500/20 flex items-center justify-center">
                      <Target class="h-5 w-5 text-amber-400" />
                    </div>
                  </div>
                  <div class="text-3xl font-bold text-white light:text-gray-900">{{ stats.potencial }}</div>
                </div>
                <div class="card-depth rounded-xl border border-white/[0.08] bg-white/[0.04] p-6 light:bg-white light:border-gray-200">
                  <div class="flex items-center justify-between pb-2">
                    <span class="text-sm font-medium text-white/50 light:text-gray-500">{{ $t('sales.statConverted') }}</span>
                    <div class="h-10 w-10 rounded-lg bg-emerald-500/20 flex items-center justify-center">
                      <CheckCircle2 class="h-5 w-5 text-emerald-400" />
                    </div>
                  </div>
                  <div class="text-3xl font-bold text-white light:text-gray-900">{{ stats.convertida }}</div>
                </div>
                <div class="card-depth rounded-xl border border-white/[0.08] bg-white/[0.04] p-6 light:bg-white light:border-gray-200">
                  <div class="flex items-center justify-between pb-2">
                    <span class="text-sm font-medium text-white/50 light:text-gray-500">{{ $t('sales.statLost') }}</span>
                    <div class="h-10 w-10 rounded-lg bg-red-500/20 flex items-center justify-center">
                      <XCircle class="h-5 w-5 text-red-400" />
                    </div>
                  </div>
                  <div class="text-3xl font-bold text-white light:text-gray-900">{{ stats.perdida }}</div>
                </div>
                <div class="card-depth rounded-xl border border-white/[0.08] bg-white/[0.04] p-6 light:bg-white light:border-gray-200">
                  <div class="flex items-center justify-between pb-2">
                    <span class="text-sm font-medium text-white/50 light:text-gray-500">{{ $t('sales.statConversion') }}</span>
                    <div class="h-10 w-10 rounded-lg bg-violet-500/20 flex items-center justify-center">
                      <Percent class="h-5 w-5 text-violet-400" />
                    </div>
                  </div>
                  <div class="text-3xl font-bold text-white light:text-gray-900">{{ stats.conversionRate.toFixed(1) }}%</div>
                </div>
                <div class="card-depth rounded-xl border border-white/[0.08] bg-white/[0.04] p-6 light:bg-white light:border-gray-200">
                  <div class="flex items-center justify-between pb-2">
                    <span class="text-sm font-medium text-white/50 light:text-gray-500">{{ $t('sales.statNoDirecionamento') }}</span>
                    <div class="h-10 w-10 rounded-lg bg-orange-500/20 flex items-center justify-center">
                      <AlertTriangle class="h-5 w-5 text-orange-400" />
                    </div>
                  </div>
                  <div class="text-3xl font-bold text-white light:text-gray-900">{{ stats.semDirecionamento }}</div>
                </div>
              </template>
            </div>

            <!-- Value totals — how much is still in the pipeline vs. already
                 closed either way, in R$ rather than a headcount. -->
            <div class="grid gap-4 md:grid-cols-3">
              <template v-if="walletLoading">
                <div v-for="i in 3" :key="i" class="rounded-xl border border-white/[0.08] bg-white/[0.02] p-6 light:bg-white light:border-gray-200">
                  <Skeleton class="h-4 w-24 mb-3 bg-white/[0.08] light:bg-gray-200" />
                  <Skeleton class="h-8 w-32 bg-white/[0.08] light:bg-gray-200" />
                </div>
              </template>
              <template v-else>
                <div class="card-depth rounded-xl border border-white/[0.08] bg-white/[0.04] p-6 light:bg-white light:border-gray-200">
                  <div class="flex items-center justify-between pb-2">
                    <span class="text-sm font-medium text-white/50 light:text-gray-500">{{ $t('sales.statValueProspect') }}</span>
                    <div class="h-10 w-10 rounded-lg bg-violet-500/20 flex items-center justify-center">
                      <Wallet class="h-5 w-5 text-violet-400" />
                    </div>
                  </div>
                  <div class="text-2xl font-bold text-white light:text-gray-900">{{ formatCurrency(stats.valorProspecto) }}</div>
                </div>
                <div class="card-depth rounded-xl border border-white/[0.08] bg-white/[0.04] p-6 light:bg-white light:border-gray-200">
                  <div class="flex items-center justify-between pb-2">
                    <span class="text-sm font-medium text-white/50 light:text-gray-500">{{ $t('sales.statValueConverted') }}</span>
                    <div class="h-10 w-10 rounded-lg bg-emerald-500/20 flex items-center justify-center">
                      <PiggyBank class="h-5 w-5 text-emerald-400" />
                    </div>
                  </div>
                  <div class="text-2xl font-bold text-white light:text-gray-900">{{ formatCurrency(stats.valorConvertido) }}</div>
                </div>
                <div class="card-depth rounded-xl border border-white/[0.08] bg-white/[0.04] p-6 light:bg-white light:border-gray-200">
                  <div class="flex items-center justify-between pb-2">
                    <span class="text-sm font-medium text-white/50 light:text-gray-500">{{ $t('sales.statValueLost') }}</span>
                    <div class="h-10 w-10 rounded-lg bg-red-500/20 flex items-center justify-center">
                      <TrendingDown class="h-5 w-5 text-red-400" />
                    </div>
                  </div>
                  <div class="text-2xl font-bold text-white light:text-gray-900">{{ formatCurrency(stats.valorPerdido) }}</div>
                </div>
              </template>
            </div>

            <Card v-if="!walletLoading && (stats.valorProspecto || stats.valorConvertido || stats.valorPerdido)" class="p-6">
              <h3 class="text-sm font-medium text-white/70 light:text-gray-600 mb-4">{{ $t('sales.valueChartTitle') }}</h3>
              <div class="h-64">
                <Doughnut :data="valueChartData" :options="valueChartOptions" />
              </div>
            </Card>

            <Card class="overflow-hidden">
              <SalesOpportunityBoard
                ref="boardRef"
                :assigned-user-id="currentUserId"
                @convert="handleConvert"
                @lose="handleLose"
                @stage-change="handleStageChange"
                @direcionamento-change="handleDirecionamentoChange"
                @details-change="handleDetailsChange"
              />
            </Card>
          </div>
        </ScrollArea>
      </TabsContent>

      <TabsContent value="closed" class="flex-1 min-h-0">
        <ScrollArea orientation="vertical" class="flex-1 h-full">
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
    </Tabs>

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
