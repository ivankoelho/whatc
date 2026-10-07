<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { DataTable, ErrorState, type Column } from '@/components/shared'
import {
  accountsService, unitsService, whatsappUsageService,
  type Unit, type WhatsAppUsageFilters, type WhatsAppUsageGroup, type WhatsAppUsageRow, type WhatsAppUsageSummary,
} from '@/services/api'
import { selectableOptions } from '@/lib/placement-options'
import { getErrorMessage } from '@/lib/api-utils'
import { ALL, USAGE_GROUPS, activeFilters, formatMoney, lastDays, type UsageGroupBy } from '@/lib/whatsapp-usage'
import { AlertTriangle, Receipt } from 'lucide-vue-next'

const { t, locale } = useI18n()

const defaults = lastDays(30)
const filters = ref({
  from: defaults.from, to: defaults.to,
  account: ALL, unit_id: ALL, agent_id: ALL, category: ALL, direction: ALL,
})
const groupBy = ref<UsageGroupBy | typeof ALL>('day')
const page = ref(1)
const pageSize = 20

const summary = ref<WhatsAppUsageSummary | null>(null)
const rows = ref<WhatsAppUsageRow[]>([])
const total = ref(0)
const isLoading = ref(true)
const loadError = ref<'forbidden' | 'failed' | null>(null)

const units = ref<Unit[]>([])
const accounts = ref<{ name: string }[]>([])
const agents = ref<{ id: string; name: string }[]>([])

// Inactive units are not offered for a new choice, but the one already selected stays.
const unitOptions = computed(() => selectableOptions(units.value, filters.value.unit_id === ALL ? null : filters.value.unit_id))

const CATEGORIES = ['utility', 'marketing', 'authentication', 'authentication_international', 'service']

function query(extra: WhatsAppUsageFilters = {}): WhatsAppUsageFilters {
  return { ...activeFilters(filters.value), ...extra } as WhatsAppUsageFilters
}

async function load() {
  isLoading.value = true
  loadError.value = null
  try {
    const [s, m] = await Promise.all([
      whatsappUsageService.summary(query(groupBy.value === ALL ? {} : { group_by: groupBy.value })),
      whatsappUsageService.messages(query({ page: page.value, limit: pageSize })),
    ])
    summary.value = s.data.data
    rows.value = m.data.data.messages
    total.value = m.data.data.total
  } catch (e: any) {
    loadError.value = e?.response?.status === 403 ? 'forbidden' : 'failed'
    if (loadError.value === 'failed') toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('whatsappUsage.title') })))
  } finally {
    isLoading.value = false
  }
}

function applyFilters() {
  page.value = 1
  load()
}

// Filter sources. Each is optional: a user without the permission just gets a shorter list.
async function loadFilterSources() {
  const [u, a, g] = await Promise.allSettled([
    unitsService.list(),
    accountsService.list(),
    whatsappUsageService.summary({ ...activeFilters({ from: filters.value.from, to: filters.value.to }), group_by: 'agent' }),
  ])
  if (u.status === 'fulfilled') units.value = u.value.data.data.units
  if (a.status === 'fulfilled') {
    const list = (a.value as any).data?.data?.accounts ?? []
    accounts.value = list.map((x: any) => ({ name: x.name }))
  }
  if (g.status === 'fulfilled') {
    agents.value = (g.value.data.data.groups ?? []).filter(x => x.key && x.label).map(x => ({ id: x.key, name: x.label as string }))
  }
}

onMounted(async () => {
  await load()
  loadFilterSources()
})

const currencyLocale = computed(() => (locale.value === 'en' ? 'en-US' : 'pt-BR'))
const money = (amount: number, currency: string) => formatMoney(amount, currency, currencyLocale.value)

const since = computed(() => {
  const s = summary.value?.recording.since
  return s ? new Date(s).toLocaleDateString(currencyLocale.value) : ''
})

interface UsageCard {
  key: string
  label: string
  value: number
  hint?: string
  tone?: 'warn'
  testid: string
}

const cards = computed<UsageCard[]>(() => {
  const c = summary.value?.counts
  if (!c) return []
  return [
    { key: 'total', label: t('whatsappUsage.totalMessages'), value: c.total, testid: 'usage-card-total' },
    { key: 'billed', label: t('whatsappUsage.billed'), value: c.billed, testid: 'usage-card-billed' },
    { key: 'not_billable', label: t('whatsappUsage.notBillable'), value: c.not_billable, testid: 'usage-card-not-billable' },
    { key: 'awaiting', label: t('whatsappUsage.awaitingPricing'), value: c.awaiting_pricing, hint: t('whatsappUsage.awaitingPricingHint'), testid: 'usage-card-awaiting' },
    { key: 'no_rate', label: t('whatsappUsage.noRate'), value: c.no_rate, hint: t('whatsappUsage.noRateHint'), tone: c.no_rate > 0 ? 'warn' : undefined, testid: 'usage-card-no-rate' },
    { key: 'unclassified', label: t('whatsappUsage.unclassified'), value: c.unclassified, hint: t('whatsappUsage.unclassifiedHint'), testid: 'usage-card-unclassified' },
    { key: 'unlinked', label: t('whatsappUsage.unlinked'), value: c.unlinked, hint: t('whatsappUsage.unlinkedHint'), tone: c.unlinked_attention > 0 ? 'warn' : undefined, testid: 'usage-card-unlinked' },
    { key: 'unconfirmed', label: t('whatsappUsage.unconfirmed'), value: c.unconfirmed, hint: t('whatsappUsage.unconfirmedHint'), testid: 'usage-card-unconfirmed' },
    { key: 'failed', label: t('whatsappUsage.failed'), value: c.failed + c.send_failed, testid: 'usage-card-failed' },
  ]
})

const groupColumns = computed<Column<WhatsAppUsageGroup>[]>(() => [
  { key: 'key', label: t('whatsappUsage.columnKey') },
  { key: 'messages', label: t('whatsappUsage.columnMessages'), align: 'right' },
  { key: 'billed', label: t('whatsappUsage.columnBilled'), align: 'right' },
  { key: 'costs', label: t('whatsappUsage.columnCost'), align: 'right' },
])

function groupLabel(g: WhatsAppUsageGroup): string {
  if (groupBy.value === 'unit') return g.label || t('whatsappUsage.noUnit')
  if (groupBy.value === 'agent') return g.label || t('whatsappUsage.noAgent')
  if (groupBy.value === 'category' && g.key === 'unknown') return t('whatsappUsage.unknownCategory')
  if (groupBy.value === 'actor') return t(`whatsappUsage.actors.${g.key}`, g.key)
  return g.key
}

const rowColumns = computed<Column<WhatsAppUsageRow>[]>(() => [
  { key: 'sent_at', label: t('whatsappUsage.columnSent') },
  { key: 'direction', label: t('whatsappUsage.columnDirection') },
  { key: 'origin', label: t('whatsappUsage.columnOrigin') },
  { key: 'unit', label: t('whatsappUsage.columnUnit') },
  { key: 'category', label: t('whatsappUsage.columnCategoryM') },
  { key: 'state', label: t('whatsappUsage.columnState') },
  { key: 'cost', label: t('whatsappUsage.columnCost'), align: 'right' },
])

const stateVariant = (s: string) => (s === 'priced' ? 'success' : s === 'no_rate' || s === 'failed' || s === 'send_failed' ? 'destructive' : 'secondary')
const when = (v: string) => new Date(v).toLocaleString(currencyLocale.value)
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <ErrorState
      v-if="loadError && !isLoading"
      :title="loadError === 'forbidden' ? $t('whatsappUsage.loadForbidden') : $t('common.loadErrorTitle')"
      :description="loadError === 'forbidden' ? '' : $t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="load"
    />

    <ScrollArea v-else orientation="vertical" class="flex-1">
      <div class="p-6">
        <div class="max-w-6xl mx-auto space-y-6">
          <p class="text-sm text-white/60 light:text-gray-600">{{ $t('whatsappUsage.subtitle') }}</p>

          <!-- Activation state: an empty table must not be read as "no consumption". -->
          <Alert v-if="summary && !summary.recording.enabled" variant="destructive" data-testid="usage-recording-off">
            <AlertTriangle class="h-4 w-4" />
            <AlertTitle>{{ $t('whatsappUsage.recordingOff') }}</AlertTitle>
            <AlertDescription>{{ $t('whatsappUsage.recordingOffHint') }}</AlertDescription>
          </Alert>
          <p v-else-if="summary && !summary.recording.since" class="text-sm text-amber-400 light:text-amber-700" data-testid="usage-recording-none">
            {{ $t('whatsappUsage.recordingNone') }}
          </p>
          <p v-else-if="summary" class="text-xs text-white/50 light:text-gray-500" data-testid="usage-recording-since">
            {{ $t('whatsappUsage.recordingSince', { date: since }) }}
          </p>

          <Card>
            <CardContent class="pt-6">
              <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
                <div class="space-y-1.5">
                  <Label class="text-xs">{{ $t('whatsappUsage.from') }}</Label>
                  <Input v-model="filters.from" type="date" data-testid="usage-filter-from" @change="applyFilters" />
                </div>
                <div class="space-y-1.5">
                  <Label class="text-xs">{{ $t('whatsappUsage.to') }}</Label>
                  <Input v-model="filters.to" type="date" data-testid="usage-filter-to" @change="applyFilters" />
                </div>
                <div v-if="accounts.length" class="space-y-1.5">
                  <Label class="text-xs">{{ $t('whatsappUsage.account') }}</Label>
                  <Select v-model="filters.account" @update:model-value="applyFilters">
                    <SelectTrigger data-testid="usage-filter-account"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem :value="ALL">{{ $t('whatsappUsage.allAccounts') }}</SelectItem>
                      <SelectItem v-for="a in accounts" :key="a.name" :value="a.name">{{ a.name }}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div v-if="units.length" class="space-y-1.5">
                  <Label class="text-xs">{{ $t('whatsappUsage.unit') }}</Label>
                  <Select v-model="filters.unit_id" @update:model-value="applyFilters">
                    <SelectTrigger data-testid="usage-filter-unit"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem :value="ALL">{{ $t('whatsappUsage.allUnits') }}</SelectItem>
                      <SelectItem v-for="u in unitOptions" :key="u.id" :value="u.id">{{ u.name }}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div v-if="agents.length" class="space-y-1.5">
                  <Label class="text-xs">{{ $t('whatsappUsage.agent') }}</Label>
                  <Select v-model="filters.agent_id" @update:model-value="applyFilters">
                    <SelectTrigger data-testid="usage-filter-agent"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem :value="ALL">{{ $t('whatsappUsage.allAgents') }}</SelectItem>
                      <SelectItem v-for="a in agents" :key="a.id" :value="a.id">{{ a.name }}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div class="space-y-1.5">
                  <Label class="text-xs">{{ $t('whatsappUsage.category') }}</Label>
                  <Select v-model="filters.category" @update:model-value="applyFilters">
                    <SelectTrigger data-testid="usage-filter-category"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem :value="ALL">{{ $t('whatsappUsage.allCategories') }}</SelectItem>
                      <SelectItem v-for="c in CATEGORIES" :key="c" :value="c">{{ c }}</SelectItem>
                      <SelectItem value="unknown">{{ $t('whatsappUsage.unknownCategory') }}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div class="space-y-1.5">
                  <Label class="text-xs">{{ $t('whatsappUsage.direction') }}</Label>
                  <Select v-model="filters.direction" @update:model-value="applyFilters">
                    <SelectTrigger data-testid="usage-filter-direction"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem :value="ALL">{{ $t('whatsappUsage.allDirections') }}</SelectItem>
                      <SelectItem value="outgoing">{{ $t('whatsappUsage.outgoing') }}</SelectItem>
                      <SelectItem value="incoming">{{ $t('whatsappUsage.incoming') }}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>
            </CardContent>
          </Card>

          <div v-if="summary" class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            <Card v-for="c in cards" :key="c.key" :data-testid="c.testid" :class="c.tone === 'warn' ? 'border-amber-500/50' : ''">
              <CardHeader class="pb-1">
                <CardDescription>{{ c.label }}</CardDescription>
                <CardTitle class="text-2xl">{{ c.value }}</CardTitle>
              </CardHeader>
              <CardContent v-if="c.hint || (c.key === 'unlinked' && summary.counts.unlinked_attention)" class="text-xs text-muted-foreground space-y-1">
                <p v-if="c.hint">{{ c.hint }}</p>
                <p v-if="c.key === 'unlinked' && summary.counts.unlinked_attention" class="text-amber-400 light:text-amber-700" data-testid="usage-unlinked-attention">
                  {{ $t('whatsappUsage.unlinkedAttention', { n: summary.counts.unlinked_attention, hours: summary.unlinked_attention_hours }) }}
                </p>
              </CardContent>
            </Card>
          </div>

          <div v-if="summary" class="grid gap-3 sm:grid-cols-2">
            <Card data-testid="usage-card-cost">
              <CardHeader class="pb-1">
                <CardDescription>{{ $t('whatsappUsage.estimatedCost') }}</CardDescription>
              </CardHeader>
              <CardContent>
                <p v-if="!summary.costs.length" class="text-sm text-muted-foreground">{{ $t('whatsappUsage.noCost') }}</p>
                <p v-for="c in summary.costs" :key="c.currency" class="text-2xl font-semibold" :data-testid="`usage-cost-${c.currency}`">{{ money(c.estimated_cost, c.currency) }}</p>
                <p class="mt-1 text-xs text-muted-foreground">{{ $t('whatsappUsage.perCurrencyNote') }}</p>
              </CardContent>
            </Card>
            <Card v-if="summary.provisional_cost.length" data-testid="usage-card-provisional">
              <CardHeader class="pb-1">
                <CardDescription>{{ $t('whatsappUsage.provisionalCost') }}</CardDescription>
              </CardHeader>
              <CardContent>
                <p v-for="c in summary.provisional_cost" :key="c.currency" class="text-2xl font-semibold text-white/70 light:text-gray-600" :data-testid="`usage-provisional-${c.currency}`">{{ money(c.estimated_cost, c.currency) }}</p>
                <p class="mt-1 text-xs text-muted-foreground">{{ $t('whatsappUsage.provisionalHint') }}</p>
              </CardContent>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <div class="flex flex-wrap items-end justify-between gap-3">
                <div>
                  <CardTitle>{{ $t('whatsappUsage.groupBy') }}</CardTitle>
                </div>
                <Select v-model="groupBy" @update:model-value="load">
                  <SelectTrigger class="w-[200px]" data-testid="usage-group-by"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem :value="ALL">{{ $t('whatsappUsage.noGroup') }}</SelectItem>
                    <SelectItem v-for="g in USAGE_GROUPS" :key="g" :value="g">{{ $t('whatsappUsage.group' + g.charAt(0).toUpperCase() + g.slice(1)) }}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </CardHeader>
            <CardContent v-if="summary?.groups">
              <DataTable :items="summary.groups" :columns="groupColumns" :is-loading="isLoading" row-key="key" :empty-icon="Receipt" :empty-title="$t('whatsappUsage.noMessages')" :empty-description="$t('whatsappUsage.noMessagesDesc')" item-name="groups">
                <template #cell-key="{ item }"><span class="font-medium">{{ groupLabel(item) }}</span></template>
                <template #cell-costs="{ item }">
                  <span v-if="!item.costs.length" class="text-muted-foreground">—</span>
                  <div v-for="c in item.costs" :key="c.currency">{{ money(c.estimated_cost, c.currency) }}</div>
                </template>
              </DataTable>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{{ $t('whatsappUsage.messagesTitle') }}</CardTitle>
              <CardDescription>{{ $t('whatsappUsage.messagesDesc') }}</CardDescription>
            </CardHeader>
            <CardContent>
              <DataTable
                :items="rows" :columns="rowColumns" :is-loading="isLoading"
                server-pagination :current-page="page" :total-items="total" :page-size="pageSize"
                :empty-icon="Receipt" :empty-title="$t('whatsappUsage.noMessages')" :empty-description="$t('whatsappUsage.noMessagesDesc')"
                item-name="messages"
                @page-change="(p: number) => { page = p; load() }"
              >
                <template #cell-sent_at="{ item }">{{ when(item.sent_at) }}</template>
                <template #cell-direction="{ item }">{{ item.direction === 'incoming' ? $t('whatsappUsage.incoming') : $t('whatsappUsage.outgoing') }}</template>
                <template #cell-origin="{ item }">
                  {{ $t(`whatsappUsage.actors.${item.actor_type}`, item.actor_type) }}
                  <span v-if="item.agent_name" class="text-muted-foreground"> · {{ item.agent_name }}</span>
                </template>
                <template #cell-unit="{ item }">{{ item.unit_name || '—' }}</template>
                <template #cell-category="{ item }">{{ item.billing_category || '—' }}</template>
                <template #cell-state="{ item }">
                  <Badge :variant="stateVariant(item.billing_state)">{{ $t(`whatsappUsage.states.${item.billing_state}`, item.billing_state) }}</Badge>
                  <Badge v-if="item.link_state === 'unlinked'" variant="outline" class="ml-1">{{ $t('whatsappUsage.unlinkedBadge') }}</Badge>
                </template>
                <template #cell-cost="{ item }">
                  <span v-if="item.estimated_cost === null" class="text-muted-foreground">—</span>
                  <span v-else>{{ money(item.estimated_cost, item.estimated_currency || '') }}</span>
                </template>
              </DataTable>
            </CardContent>
          </Card>
        </div>
      </div>
    </ScrollArea>
  </div>
</template>
