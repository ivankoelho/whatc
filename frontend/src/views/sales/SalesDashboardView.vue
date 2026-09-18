<script setup lang="ts">
// Thin managerial dashboard for the sales funnel: reuses the generic widget
// engine (GET /widgets + GET /widgets/data, same as DashboardView.vue)
// filtered to data_source === 'sales_opportunities', and reuses the same
// DateRangePicker/useDateRange composable as the main dashboard. Unlike
// DashboardView.vue there is no widget CRUD/drag-resize here — the 5 cards
// are seeded server-side by ensureDefaultSalesOpportunityWidgets and this
// view only displays their computed values as simple stat cards (no
// chart/table rendering), since every default widget's display_type is
// "number" and the widget engine always computes `value` regardless of
// display_type.
import { ref, computed, onMounted, watch } from 'vue'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { PageHeader, DateRangePicker, ErrorState } from '@/components/shared'
import { widgetsService, type DashboardWidget, type WidgetData } from '@/services/api'
import { useDateRange } from '@/composables/useDateRange'
import { BarChart3, TrendingUp, TrendingDown, Minus } from 'lucide-vue-next'

const widgets = ref<DashboardWidget[]>([])
const widgetData = ref<Record<string, WidgetData>>({})
const isLoading = ref(true)
const isDataLoading = ref(false)
const loadFailed = ref(false)

const salesWidgets = computed(() => widgets.value.filter(w => w.data_source === 'sales_opportunities'))

const {
  selectedRange,
  customDateRange,
  isDatePickerOpen,
  dateRange,
  formatDateRangeDisplay,
  applyCustomRange: applyCustomRangeBase,
} = useDateRange({ storageKey: 'sales_dashboard' })

const colorMap: Record<string, { bg: string; text: string; bar: string }> = {
  blue: { bg: 'bg-blue-500/20', text: 'text-blue-400', bar: 'bg-gradient-to-r from-blue-500/60 to-blue-500/0' },
  green: { bg: 'bg-emerald-500/20', text: 'text-emerald-400', bar: 'bg-gradient-to-r from-emerald-500/60 to-emerald-500/0' },
  purple: { bg: 'bg-violet-500/20', text: 'text-violet-400', bar: 'bg-gradient-to-r from-violet-500/60 to-violet-500/0' },
  orange: { bg: 'bg-orange-500/20', text: 'text-orange-400', bar: 'bg-gradient-to-r from-orange-500/60 to-orange-500/0' },
  red: { bg: 'bg-red-500/20', text: 'text-red-400', bar: 'bg-gradient-to-r from-red-500/60 to-red-500/0' },
  cyan: { bg: 'bg-cyan-500/20', text: 'text-cyan-400', bar: 'bg-gradient-to-r from-cyan-500/60 to-cyan-500/0' },
}
const getColor = (color: string) => colorMap[color] || colorMap.blue

const formatValue = (widget: DashboardWidget, value: number): string => {
  if (widget.metric === 'sum' && widget.field === 'estimated_value') {
    return new Intl.NumberFormat('pt-BR', { style: 'currency', currency: 'BRL', maximumFractionDigits: 0 }).format(value)
  }
  if (value >= 1000000) return (value / 1000000).toFixed(1) + 'M'
  if (value >= 1000) return (value / 1000).toFixed(1) + 'K'
  return Math.round(value).toString()
}

async function fetchWidgets() {
  const response = await widgetsService.list()
  widgets.value = (response.data as any).data?.widgets || []
}

async function fetchWidgetData() {
  if (salesWidgets.value.length === 0) return
  isDataLoading.value = true
  try {
    const { from, to } = dateRange.value
    const response = await widgetsService.getAllData({ from, to })
    widgetData.value = (response.data as any).data?.data || {}
  } finally {
    isDataLoading.value = false
  }
}

async function loadDashboard() {
  isLoading.value = true
  loadFailed.value = false
  try {
    await fetchWidgets()
    await fetchWidgetData()
  } catch (e) {
    loadFailed.value = true
  } finally {
    isLoading.value = false
  }
}

const applyCustomRange = () => {
  applyCustomRangeBase()
  fetchWidgetData()
}

watch(selectedRange, (newValue) => {
  if (newValue !== 'custom') fetchWidgetData()
})

onMounted(loadDashboard)
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader
      :title="$t('sales.dashboardTitle')"
      :description="$t('sales.dashboardSubtitle')"
      :icon="BarChart3"
      icon-gradient="bg-gradient-to-br from-emerald-500 to-teal-600 shadow-emerald-500/20"
    >
      <template #actions>
        <DateRangePicker
          v-model:selected-range="selectedRange"
          v-model:custom-date-range="customDateRange"
          v-model:is-date-picker-open="isDatePickerOpen"
          :format-date-range-display="formatDateRangeDisplay"
          @apply-custom="applyCustomRange"
        />
      </template>
    </PageHeader>

    <ErrorState
      v-if="loadFailed"
      :title="$t('common.loadErrorTitle')"
      :description="$t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="loadDashboard"
    />
    <ScrollArea v-else class="flex-1">
      <div class="p-6">
        <div v-if="isLoading" class="grid gap-4 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-5">
          <div v-for="i in 5" :key="i" class="rounded-xl border border-white/[0.08] bg-white/[0.02] p-6 light:bg-white light:border-gray-200">
            <Skeleton class="h-4 w-24 mb-3 bg-white/[0.08] light:bg-gray-200" />
            <Skeleton class="h-8 w-20 bg-white/[0.08] light:bg-gray-200" />
          </div>
        </div>

        <div v-else-if="salesWidgets.length === 0" class="text-sm text-white/50 light:text-gray-500">
          {{ $t('sales.dashboardEmpty') }}
        </div>

        <div v-else class="grid gap-4 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-5">
          <div
            v-for="widget in salesWidgets"
            :key="widget.id"
            class="group relative card-depth rounded-xl border border-white/[0.08] bg-white/[0.04] p-6 light:bg-white light:border-gray-200 overflow-hidden"
          >
            <div :class="['absolute top-0 inset-x-0 h-0.5', getColor(widget.color).bar]" />

            <div class="flex flex-row items-start justify-between space-y-0 pb-2">
              <span class="text-sm font-medium text-white/50 light:text-gray-500">{{ widget.name }}</span>
              <div :class="['h-10 w-10 rounded-lg flex items-center justify-center shrink-0', getColor(widget.color).bg]">
                <BarChart3 :class="['h-5 w-5', getColor(widget.color).text]" />
              </div>
            </div>

            <div class="pt-2">
              <div class="text-3xl font-bold text-white light:text-gray-900">
                <template v-if="isDataLoading">
                  <Skeleton class="h-8 w-20 bg-white/[0.08] light:bg-gray-200" />
                </template>
                <template v-else>
                  {{ formatValue(widget, widgetData[widget.id]?.value || 0) }}
                </template>
              </div>
              <p v-if="widget.description" class="text-xs text-white/30 light:text-gray-400 mt-1">{{ widget.description }}</p>
              <div v-if="widget.show_change && widgetData[widget.id] && !isDataLoading" class="flex items-center text-xs text-white/40 light:text-gray-500 mt-1">
                <component
                  :is="(widgetData[widget.id]?.change ?? 0) > 0 ? TrendingUp : (widgetData[widget.id]?.change ?? 0) < 0 ? TrendingDown : Minus"
                  :class="[
                    'h-3 w-3 mr-1',
                    (widgetData[widget.id]?.change ?? 0) > 0 ? 'text-emerald-400' : (widgetData[widget.id]?.change ?? 0) < 0 ? 'text-red-400' : 'text-white/30'
                  ]"
                />
                <span :class="(widgetData[widget.id]?.change ?? 0) > 0 ? 'text-emerald-400' : (widgetData[widget.id]?.change ?? 0) < 0 ? 'text-red-400' : 'text-white/30 light:text-gray-400'">
                  {{ Math.abs(widgetData[widget.id]?.change || 0).toFixed(1) }}%
                </span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </ScrollArea>
  </div>
</template>
