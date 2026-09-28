<script setup lang="ts">
import { ref, onMounted } from 'vue'
import axios from 'axios'
import draggable from 'vuedraggable'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { getErrorMessage } from '@/lib/api-utils'
import { Spinner } from '@/components/ui/spinner'
import { Button } from '@/components/ui/button'
import { salesOpportunitiesService } from '@/services/api'
import type { SalesOpportunity, SalesOpportunityStage } from '@/services/api'
import SalesOpportunityCard from './SalesOpportunityCard.vue'

const props = defineProps<{ assignedUserId?: string }>()

const emit = defineEmits<{
  convert: [opportunity: SalesOpportunity]
  lose: [opportunity: SalesOpportunity]
  'stage-change': [opportunity: SalesOpportunity, stage: SalesOpportunityStage]
  'direcionamento-change': [opportunity: SalesOpportunity]
  'details-change': [opportunity: SalesOpportunity]
}>()

const { t } = useI18n()

// The 3 funnel stages plus 2 terminal outcomes — Convertida/Perdida are
// status, not stage, but the board renders them as columns too so a card
// visibly moves there instead of just vanishing (spec item 11's board
// restructuring). ColumnKey covers both kinds; STAGE_KEYS is the subset
// that changeStage() actually accepts.
type ColumnKey = SalesOpportunityStage | 'convertida' | 'perdida'
const COLUMN_DEFS: { key: ColumnKey; labelKey: string; accent: string }[] = [
  { key: 'potencial', labelKey: 'sales.stagePotencial', accent: '' },
  { key: 'abrir_orcamento', labelKey: 'sales.stageAbrirOrcamento', accent: '' },
  { key: 'direcionada', labelKey: 'sales.stageDirecionada', accent: '' },
  { key: 'convertida', labelKey: 'sales.statusConvertida', accent: 'border-t-2 border-t-emerald-500/60' },
  { key: 'perdida', labelKey: 'sales.statusPerdida', accent: 'border-t-2 border-t-red-500/60' },
]

// A page per column, fetched server-side-filtered — replaces the old
// single "up to 100 across every column" fetch (see git history), which
// grew the whole board (and the page around it) tall instead of giving
// each column its own scroll.
const PAGE_SIZE = 20

interface ColumnState {
  key: ColumnKey
  items: SalesOpportunity[]
  offset: number
  total: number
  hasMore: boolean
  loadingMore: boolean
}

// Every opportunity belongs to exactly one column: its stage while aberta,
// or its terminal status once closed. Cancelada (X2-cancelled-after-convert)
// has no column of its own — it stays visible in "Minhas vendas fechadas"
// instead; folding it into Perdida would misrepresent a completed sale.
function columnKeyFor(o: SalesOpportunity): ColumnKey | null {
  if (o.status === 'convertida') return 'convertida'
  if (o.status === 'perdida') return 'perdida'
  if (o.status === 'aberta') return o.stage
  return null
}

function emptyColumn(key: ColumnKey): ColumnState {
  return { key, items: [], offset: 0, total: 0, hasMore: false, loadingMore: false }
}

function paramsFor(key: ColumnKey, offset: number): Record<string, string> {
  const params: Record<string, string> = { limit: String(PAGE_SIZE), offset: String(offset) }
  if (props.assignedUserId) params.assigned_user_id = props.assignedUserId
  if (key === 'convertida' || key === 'perdida') {
    params.status = key
  } else {
    params.status = 'aberta'
    params.stage = key
  }
  return params
}

const columns = ref<ColumnState[]>(COLUMN_DEFS.map(c => emptyColumn(c.key)))
const loading = ref(false)
const failed = ref(false)

/** Ocorrências com requisição em voo não aceitam novo arrasto. */
const pending = ref(new Set<string>())

/** Origem do arrasto, capturada no início e usada na reversão. */
let dragOrigin: { opportunityId: string; fromKey: ColumnKey } | null = null

async function loadAll() {
  loading.value = true
  failed.value = false
  try {
    const results = await Promise.all(COLUMN_DEFS.map(c => salesOpportunitiesService.list(paramsFor(c.key, 0))))
    columns.value = COLUMN_DEFS.map((c, i) => {
      const { opportunities, total, has_more } = results[i].data.data
      return { key: c.key, items: opportunities, offset: opportunities.length, total, hasMore: has_more, loadingMore: false }
    })
  } catch {
    failed.value = true
  } finally {
    loading.value = false
  }
}

async function loadMore(col: ColumnState) {
  col.loadingMore = true
  try {
    const { data } = await salesOpportunitiesService.list(paramsFor(col.key, col.offset))
    col.items.push(...data.data.opportunities)
    col.offset += data.data.opportunities.length
    col.total = data.data.total
    col.hasMore = data.data.has_more
  } catch (e) {
    toast.error(getErrorMessage(e, t('sales.boardLoadFailed')))
  } finally {
    col.loadingMore = false
  }
}

function onDragStart(col: ColumnState, evt: { oldIndex: number }) {
  const item = col.items[evt.oldIndex]
  dragOrigin = item ? { opportunityId: item.id, fromKey: col.key } : null
}

function canMove(evt: { draggedContext: { element: SalesOpportunity } }): boolean {
  return !pending.value.has(evt.draggedContext.element.id)
}

async function onColumnChange(toCol: ColumnState, evt: { added?: { element: SalesOpportunity } }) {
  if (!evt.added) return // reordenar dentro da mesma coluna não muda nada no servidor

  const origin = dragOrigin
  dragOrigin = null
  if (!origin) return

  const opp = evt.added.element
  if (origin.fromKey === toCol.key) return // guarda explícita do no-op

  const fromCol = columns.value.find(c => c.key === origin.fromKey)

  // Convertida/Perdida são desfechos, não etapas do funil — soltar aqui não
  // chama a API diretamente (Perder exige motivo obrigatório via diálogo;
  // Converter deve se comportar igual, seja pelo botão ou pelo arrasto).
  // Volta o card na hora pra origem e dispara o mesmo fluxo que os botões já
  // usam; se a ação for concluída, o card reaparece aqui após o refresh do
  // board feito pelo componente pai.
  if (toCol.key === 'convertida' || toCol.key === 'perdida') {
    const idx = toCol.items.findIndex(i => i.id === opp.id)
    if (idx !== -1) toCol.items.splice(idx, 1)
    if (fromCol) fromCol.items.push(opp)
    if (toCol.key === 'convertida') emit('convert', opp)
    else emit('lose', opp)
    return
  }

  const targetStage = toCol.key as SalesOpportunityStage
  pending.value.add(opp.id)
  try {
    const { data } = await salesOpportunitiesService.changeStage(opp.id, targetStage)
    const updated = data.data
    const idx = toCol.items.findIndex(i => i.id === opp.id)
    if (idx !== -1) toCol.items.splice(idx, 1, updated)
    emit('stage-change', updated, targetStage)
  } catch (e) {
    // Reversão pela origem guardada, não pelo que está na tela.
    const idx = toCol.items.findIndex(i => i.id === opp.id)
    if (idx !== -1) toCol.items.splice(idx, 1)
    if (fromCol) fromCol.items.push(opp)

    // Entrar em "direcionada" sem direcionamento é a única falha de validação
    // realista neste board (etapa e status já são controlados pelo próprio
    // board), então um 400 ao soltar ali é tratado como esse erro específico
    // em vez do texto genérico do servidor.
    const status = axios.isAxiosError(e) ? e.response?.status : undefined
    if (status === 400 && targetStage === 'direcionada') {
      toast.error(t('sales.direcionamentoRequired'))
    } else {
      toast.error(getErrorMessage(e, t('sales.stageChangeFailed')))
    }
  } finally {
    pending.value.delete(opp.id)
  }
}

function onDirecionamentoChange(updated: SalesOpportunity) {
  const key = columnKeyFor(updated)
  const col = columns.value.find(c => c.key === key)
  const idx = col?.items.findIndex(i => i.id === updated.id)
  if (col && idx !== undefined && idx !== -1) col.items.splice(idx, 1, updated)
  emit('direcionamento-change', updated)
}

function onDetailsChange(updated: SalesOpportunity) {
  const key = columnKeyFor(updated)
  const col = columns.value.find(c => c.key === key)
  const idx = col?.items.findIndex(i => i.id === updated.id)
  if (col && idx !== undefined && idx !== -1) col.items.splice(idx, 1, updated)
  emit('details-change', updated)
}

onMounted(loadAll)

defineExpose({ refresh: loadAll })
</script>

<template>
  <div id="sales-opportunities-board" class="flex gap-4 overflow-x-auto p-4">
    <div v-if="failed" class="p-3 text-center w-full">
      <p class="text-xs text-muted-foreground">{{ $t('sales.boardLoadFailed') }}</p>
      <Button variant="outline" size="sm" class="mt-2" @click="loadAll">{{ $t('common.retryLoad') }}</Button>
    </div>

    <template v-else>
      <div
        v-for="col in columns"
        :key="col.key"
        :data-board-column="col.key"
        :class="['flex w-72 shrink-0 flex-col rounded-lg border border-white/[0.08] light:border-gray-200 bg-white/[0.02] light:bg-gray-50', COLUMN_DEFS.find(c => c.key === col.key)?.accent]"
      >
        <div class="flex items-center justify-between gap-2 border-b border-white/[0.08] light:border-gray-200 p-3 shrink-0">
          <span class="text-sm font-medium">{{ $t(COLUMN_DEFS.find(c => c.key === col.key)!.labelKey) }}</span>
          <span class="text-xs text-muted-foreground" data-board-column-count>{{ col.total }}</span>
        </div>

        <!-- Fixed column height, own scrollbar — the board (and the page
             around it) no longer grows with the column's content. -->
        <div class="flex flex-1 flex-col gap-2 p-2 min-h-24 max-h-[65vh] overflow-y-auto">
          <!-- flex-1 so the drop target always fills the column's free
               height, even with 0 items — it used to collapse to 0px tall
               when empty (a bare v-for renders no natural height), leaving
               "Nenhuma oportunidade" floating below it as plain text, not
               inside the actual dropzone, so a card could only be dropped
               right at the very top edge next to the header. The empty
               message now lives in the #footer slot, inside this same
               sortable root, instead of as a sibling. -->
          <draggable
            v-model="col.items"
            :group="{ name: 'sales-opportunities' }"
            :move="canMove"
            :disabled="col.key === 'convertida' || col.key === 'perdida'"
            item-key="id"
            data-board-dropzone
            class="flex flex-1 flex-col gap-2"
            @start="onDragStart(col, $event)"
            @change="onColumnChange(col, $event)"
            @end="dragOrigin = null"
          >
            <template #item="{ element }">
              <SalesOpportunityCard
                :opportunity="element"
                :disabled="pending.has(element.id)"
                @convert="emit('convert', $event)"
                @lose="emit('lose', $event)"
                @direcionamento-change="onDirecionamentoChange"
                @details-change="onDetailsChange"
              />
            </template>
            <template #footer>
              <div v-if="loading" class="flex justify-center p-3">
                <Spinner class="h-4 w-4" />
              </div>
              <p v-else-if="col.items.length === 0" class="p-3 text-center text-xs text-muted-foreground">
                {{ $t('sales.columnEmpty') }}
              </p>
            </template>
          </draggable>

          <Button
            v-if="col.hasMore"
            data-board-load-more
            variant="ghost"
            size="sm"
            class="w-full text-xs shrink-0"
            :disabled="col.loadingMore"
            @click="loadMore(col)"
          >
            <Spinner v-if="col.loadingMore" class="h-3 w-3 mr-1.5" />
            {{ $t('sales.loadMore') }} ({{ col.total - col.items.length }})
          </Button>
        </div>
      </div>
    </template>
  </div>
</template>
