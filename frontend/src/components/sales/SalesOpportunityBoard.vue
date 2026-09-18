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
}>()

const { t } = useI18n()

const STAGES: SalesOpportunityStage[] = ['potencial', 'abrir_orcamento', 'direcionada']
const STAGE_LABEL_KEY: Record<SalesOpportunityStage, string> = {
  potencial: 'sales.stagePotencial',
  abrir_orcamento: 'sales.stageAbrirOrcamento',
  direcionada: 'sales.stageDirecionada',
}

interface ColumnState {
  stage: SalesOpportunityStage
  items: SalesOpportunity[]
}

const columns = ref<ColumnState[]>(STAGES.map(stage => ({ stage, items: [] })))
const loading = ref(false)
const failed = ref(false)

/** Ocorrências com requisição em voo não aceitam novo arrasto. */
const pending = ref(new Set<string>())

/** Origem do arrasto, capturada no início e usada na reversão. */
let dragOrigin: { opportunityId: string; fromStage: SalesOpportunityStage } | null = null

// ponytail: uma única página de até 100 abertas por escopo (o board só
// mostra status=aberta — fechadas vivem na aba "Minhas vendas fechadas"), sem
// paginação por coluna como OccurrenceBoard.vue. Suficiente pro "primeiro
// corte" da carteira de um agente; adicionar paginação por coluna se o volume
// por vendedor crescer além disso.
async function loadAll() {
  loading.value = true
  failed.value = false
  try {
    const params: Record<string, string> = { status: 'aberta', limit: '100' }
    if (props.assignedUserId) params.assigned_user_id = props.assignedUserId
    const { data } = await salesOpportunitiesService.list(params)
    const opportunities = data.data.opportunities
    columns.value = STAGES.map(stage => ({
      stage,
      items: opportunities.filter(o => o.stage === stage),
    }))
  } catch {
    failed.value = true
  } finally {
    loading.value = false
  }
}

function onDragStart(col: ColumnState, evt: { oldIndex: number }) {
  const item = col.items[evt.oldIndex]
  dragOrigin = item ? { opportunityId: item.id, fromStage: col.stage } : null
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
  if (origin.fromStage === toCol.stage) return // guarda explícita do no-op

  const fromCol = columns.value.find(c => c.stage === origin.fromStage)

  pending.value.add(opp.id)
  try {
    const { data } = await salesOpportunitiesService.changeStage(opp.id, toCol.stage)
    const updated = data.data
    const idx = toCol.items.findIndex(i => i.id === opp.id)
    if (idx !== -1) toCol.items.splice(idx, 1, updated)
    emit('stage-change', updated, toCol.stage)
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
    if (status === 400 && toCol.stage === 'direcionada') {
      toast.error(t('sales.direcionamentoRequired'))
    } else {
      toast.error(getErrorMessage(e, t('sales.stageChangeFailed')))
    }
  } finally {
    pending.value.delete(opp.id)
  }
}

function onDirecionamentoChange(updated: SalesOpportunity) {
  const col = columns.value.find(c => c.stage === updated.stage)
  const idx = col?.items.findIndex(i => i.id === updated.id)
  if (col && idx !== undefined && idx !== -1) col.items.splice(idx, 1, updated)
  emit('direcionamento-change', updated)
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
        :key="col.stage"
        :data-board-column="col.stage"
        class="flex w-72 shrink-0 flex-col rounded-lg border border-white/[0.08] light:border-gray-200 bg-white/[0.02] light:bg-gray-50"
      >
        <div class="flex items-center justify-between gap-2 border-b border-white/[0.08] light:border-gray-200 p-3">
          <span class="text-sm font-medium">{{ $t(STAGE_LABEL_KEY[col.stage]) }}</span>
          <span class="text-xs text-muted-foreground" data-board-column-count>{{ col.items.length }}</span>
        </div>

        <div class="flex flex-1 flex-col gap-2 p-2 min-h-24">
          <draggable
            v-model="col.items"
            :group="{ name: 'sales-opportunities' }"
            :move="canMove"
            item-key="id"
            data-board-dropzone
            class="flex flex-1 flex-col gap-2 min-h-16"
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
              />
            </template>
          </draggable>

          <div v-if="loading" class="flex justify-center p-3">
            <Spinner class="h-4 w-4" />
          </div>

          <p v-else-if="col.items.length === 0" class="p-3 text-center text-xs text-muted-foreground">
            {{ $t('sales.columnEmpty') }}
          </p>
        </div>
      </div>
    </template>
  </div>
</template>
