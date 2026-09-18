<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Card } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Skeleton } from '@/components/ui/skeleton'
import { PageHeader, DataTable, ErrorState, type Column } from '@/components/shared'
import { TrendingUp, Briefcase, Target, CheckCircle2, XCircle, Percent, AlertTriangle } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { salesOpportunitiesService } from '@/services/api'
import type { SalesOpportunity } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDate } from '@/lib/utils'
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

const stats = computed(() => {
  const total = wallet.value.length
  const potencial = wallet.value.filter(o => o.stage === 'potencial').length
  const convertida = wallet.value.filter(o => o.status === 'convertida').length
  const perdida = wallet.value.filter(o => o.status === 'perdida').length
  const semDirecionamento = wallet.value.filter(o => o.status === 'aberta' && !o.direcionamento).length
  const closedTotal = convertida + perdida
  const conversionRate = closedTotal > 0 ? (convertida / closedTotal) * 100 : 0
  return { total, potencial, convertida, perdida, semDirecionamento, conversionRate }
})

const closedOpportunities = computed(() =>
  wallet.value.filter(o => o.status === 'convertida' || o.status === 'perdida')
)

const closedColumns = computed<Column<SalesOpportunity>[]>(() => [
  { key: 'opportunity_number', label: t('sales.columnOpportunityNumber') },
  { key: 'contact', label: t('sales.columnContact') },
  { key: 'estimated_value', label: t('sales.columnEstimatedValue') },
  { key: 'status', label: t('sales.columnStatus') },
  { key: 'closed_at', label: t('sales.columnClosedDate') },
  { key: 'loss_reason', label: t('sales.columnLossReason') },
])

function formatCurrency(value?: number): string {
  if (value == null) return '—'
  return new Intl.NumberFormat('pt-BR', { style: 'currency', currency: 'BRL' }).format(value)
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

onMounted(fetchWallet)
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('sales.title')" :description="$t('sales.subtitle')" :icon="TrendingUp" icon-gradient="bg-gradient-to-br from-emerald-500 to-teal-600 shadow-emerald-500/20" />

    <Tabs v-model="activeTab" class="flex-1 min-h-0 flex flex-col">
      <div class="px-6 pt-4">
        <TabsList>
          <TabsTrigger value="wallet">{{ $t('sales.tabMyWallet') }}</TabsTrigger>
          <TabsTrigger value="closed">{{ $t('sales.tabMyClosedSales') }}</TabsTrigger>
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

            <Card class="overflow-hidden">
              <SalesOpportunityBoard
                ref="boardRef"
                :assigned-user-id="currentUserId"
                @convert="handleConvert"
                @lose="handleLose"
                @stage-change="handleStageChange"
                @direcionamento-change="handleDirecionamentoChange"
              />
            </Card>
          </div>
        </ScrollArea>
      </TabsContent>

      <TabsContent value="closed" class="flex-1 min-h-0">
        <ScrollArea orientation="vertical" class="flex-1 h-full">
          <div class="p-6">
            <p class="text-sm text-white/50 light:text-gray-500 mb-4">{{ $t('sales.manualConversionNotice') }}</p>
            <Card>
              <DataTable
                :items="closedOpportunities"
                :columns="closedColumns"
                :is-loading="walletLoading"
                :empty-icon="TrendingUp"
                :empty-title="$t('sales.noClosedSales')"
                item-name="closedSales"
              >
                <template #cell-contact="{ item }">
                  {{ item.contact?.profile_name || item.contact_id }}
                </template>
                <template #cell-estimated_value="{ item }">
                  {{ formatCurrency(item.estimated_value) }}
                </template>
                <template #cell-status="{ item }">
                  <Badge :variant="item.status === 'convertida' ? 'success' : 'destructive'">
                    {{ item.status === 'convertida' ? $t('sales.statusConvertida') : $t('sales.statusPerdida') }}
                  </Badge>
                </template>
                <template #cell-closed_at="{ item }">
                  {{ formatDate(item.converted_at || item.lost_at || item.stage_changed_at) }}
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
  </div>
</template>
