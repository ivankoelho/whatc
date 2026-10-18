<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { watchDebounced } from '@vueuse/core'
import { useRouter } from 'vue-router'
import draggable from 'vuedraggable'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { getErrorMessage } from '@/lib/api-utils'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { useOccurrencesStore } from '@/stores/occurrences'
import { wsService } from '@/services/websocket'
import type { Occurrence, OccurrenceStage } from '@/services/api'
import OccurrenceCard from './OccurrenceCard.vue'
import { byOpenedAtDesc, cardOrder, placePreview } from '@/lib/board-order'

const props = defineProps<{ protocol?: string }>()

const store = useOccurrencesStore()
const router = useRouter()
const { t } = useI18n()

// A lista já navega ao clicar numa linha (OccurrencesView.vue); o quadro
// precisa do mesmo caminho para o cartão abrir o detalhe.
function goToDetail(occurrence: Occurrence) {
  // Um cartão travado (requisição em voo) não navega: o detalhe renderizaria
  // a etapa antiga enquanto o PUT ainda está mudando ela por baixo.
  if (pending.value.has(occurrence.id)) return
  router.push({ name: 'occurrence-detail', params: { id: occurrence.id } })
}

/** Ocorrências com requisição em voo. Um cartão travado não aceita novo arrasto. */
const pending = ref(new Set<string>())

/**
 * A origem do arrasto, capturada no início e usada na reversão. Guardar isto
 * explicitamente é o que permite desfazer sem depender do estado visual.
 */
let dragOrigin: { occurrenceId: string; fromStageId: string } | null = null

/** Cartões por página, em cada coluna. */
const PAGE_SIZE = 25
/** Janela da coluna de fechamento. Constante desta fase, não configuração. */
const CLOSED_WINDOW_DAYS = 7

interface ColumnState {
  stage: OccurrenceStage
  items: Occurrence[]
  total: number
  page: number
  loading: boolean
  failed: boolean
}

const columns = ref<ColumnState[]>([])

/**
 * Etapa normal mostra as abertas; etapa de fechamento mostra as fechadas na
 * janela recente. Nunca use `open=false` para a segunda: o handler testa
 * `open == "true"`, então `false` significa *sem filtro* e traria tudo.
 *
 * O corte vai absoluto, em RFC3339, calculado aqui — o backend não sabe o que
 * são "7 dias", ele recebe uma data e compara.
 */
function columnParams(stage: OccurrenceStage, page: number): Record<string, string> {
  const params: Record<string, string> = {
    stage_id: stage.id,
    page: String(page),
    limit: String(PAGE_SIZE),
  }

  if (stage.is_closing) {
    params.closed_since = new Date(Date.now() - CLOSED_WINDOW_DAYS * 24 * 60 * 60 * 1000).toISOString()
  } else {
    params.open = 'true'
  }

  if (props.protocol) params.protocol = props.protocol

  return params
}

async function loadColumn(col: ColumnState, page: number) {
  if (col.loading) return // reentrancy guard: blocks a double-click from double-appending the same page
  col.loading = true
  col.failed = false
  try {
    const { occurrences, total } = await store.fetchColumn(columnParams(col.stage, page))
    col.items = page === 1 ? occurrences : [...col.items, ...occurrences]
    col.total = total
    col.page = page
  } catch {
    // Falha é isolada por coluna: esta avisa, as outras seguem funcionando.
    col.failed = true
  } finally {
    col.loading = false
  }
}

/**
 * Dispara todas as colunas juntas. O tempo de abertura é o da chamada mais
 * lenta, não a soma. `loadColumn` trata o próprio erro, então o Promise.all
 * nunca rejeita — é isso que mantém o isolamento por coluna.
 */
async function loadAll() {
  columns.value = store.stages.map(stage => ({
    stage,
    items: [],
    total: 0,
    page: 1,
    loading: false, // loadColumn sets this synchronously below, before Vue paints
    failed: false,
  }))
  await Promise.all(columns.value.map(col => loadColumn(col, 1)))
}

function hasMore(col: ColumnState): boolean {
  return col.items.length < col.total
}

/**
 * Aplica um occurrence_changed vindo de qualquer origem (criação, edição ou
 * mudança de etapa, por este cliente ou por outro) contra as colunas já
 * carregadas.
 *
 * Uma ocorrência já conhecida é atualizada no lugar, ou movida de coluna se a
 * etapa mudou — mantendo a ordenação por opened_at, o mesmo invariante que o
 * arrastar-e-soltar garante depois de um movimento bem-sucedido. Uma
 * ocorrência desconhecida só soma no total do cabeçalho, sem inserir cartão
 * fora da ordem de paginação (spec §2, regra da criação).
 *
 * ponytail: o payload não diz se a origem foi CreateOccurrence ou
 * UpdateOccurrence/ChangeOccurrenceStage — as três emitem o mesmo tipo de
 * mensagem. Uma ocorrência desconhecida por estar apenas fora da página
 * carregada (não por ser nova) também cai no ramo "soma o total", o que pode
 * super-contar por um até o próximo "Load More" ou recarregamento da coluna,
 * que sempre resincroniza `total` pela resposta do servidor. Sem esse sinal
 * extra no payload não há como distinguir os dois casos no cliente.
 */
function handleOccurrenceChanged(payload: Occurrence) {
  const targetCol = columns.value.find(c => c.stage.id === payload.stage_id)
  if (!targetCol) return // etapa sem coluna carregada ainda

  let sourceCol: ColumnState | null = null
  let sourceIdx = -1
  for (const col of columns.value) {
    const idx = col.items.findIndex(i => i.id === payload.id)
    if (idx !== -1) {
      sourceCol = col
      sourceIdx = idx
      break
    }
  }

  if (!sourceCol) {
    targetCol.total += 1
    return
  }

  if (sourceCol.stage.id === targetCol.stage.id) {
    sourceCol.items.splice(sourceIdx, 1, payload)
    sortByOpenedAtDesc(sourceCol.items)
    return
  }

  sourceCol.items.splice(sourceIdx, 1)
  sourceCol.total = Math.max(0, sourceCol.total - 1)
  targetCol.items.push(payload)
  targetCol.total += 1
  sortByOpenedAtDesc(targetCol.items)
}

let unsubscribeOccurrenceChanged: (() => void) | null = null

function onDragStart(col: ColumnState, evt: { oldIndex: number }) {
  const item = col.items[evt.oldIndex]
  dragOrigin = item ? { occurrenceId: item.id, fromStageId: col.stage.id } : null
}

/** Recusa arrastar um cartão cuja própria requisição ainda está em voo, e
 * mostra o espaço do cartão já no lugar onde ele vai cair (opened_at DESC),
 * em vez de sob o ponteiro — senão ele pula ao soltar. */
function canMove(evt: { draggedContext: { element: Occurrence }; relatedContext?: { list?: Occurrence[] }; dragged?: HTMLElement }): boolean {
  if (pending.value.has(evt.draggedContext.element.id)) return false
  if (evt.relatedContext?.list) placePreview(evt.dragged, evt.relatedContext.list, evt.draggedContext.element)
  return true
}

function sortByOpenedAtDesc(items: Occurrence[]) {
  items.sort(byOpenedAtDesc)
}

async function onColumnChange(toCol: ColumnState, evt: { added?: { element: Occurrence } }) {
  // Mover dentro da mesma coluna emite `moved`, não `added`: nenhuma
  // requisição sai e nenhum evento de timeline é criado. A ordem, porém,
  // nunca é manual — nem no código nem no estado — então a coluna volta a
  // ficar por opened_at DESC mesmo sem chamada ao servidor.
  if (!evt.added) {
    sortByOpenedAtDesc(toCol.items)
    return
  }

  const origin = dragOrigin
  dragOrigin = null
  if (!origin) return

  const occ = evt.added.element

  // Guarda explícita do no-op, exigida pela spec e não deixada por conta do
  // comportamento da biblioteca.
  if (origin.fromStageId === toCol.stage.id) return

  const fromCol = columns.value.find(c => c.stage.id === origin.fromStageId)

  // Ordena já no drop, não depois do PUT resolver: senão o cartão fica na
  // posição bruta do drop pela ida-e-volta inteira e só pula pro lugar certo
  // quando a requisição termina — sob o cursor, logo depois de soltar. O
  // caminho na mesma coluna (acima) já ordena de forma síncrona; deferir só
  // o caso entre colunas faria um gesto encaixar e o outro pular.
  sortByOpenedAtDesc(toCol.items)

  pending.value.add(occ.id)
  try {
    await store.moveStage(occ.id, toCol.stage.id)
    // Só ajusta as contagens em caso de sucesso. Durante a requisição, o
    // destino mostra N cartões sob um cabeçalho com N-1 — deliberado: é o
    // que permite a reversão do catch abaixo dispensar desfazer contagem.
    toCol.total += 1
    if (fromCol) fromCol.total = Math.max(0, fromCol.total - 1)
  } catch (e) {
    // Reversão pela origem guardada, não pelo que está na tela.
    const idx = toCol.items.findIndex(i => i.id === occ.id)
    if (idx !== -1) toCol.items.splice(idx, 1)
    if (fromCol) {
      fromCol.items.push(occ)
      sortByOpenedAtDesc(fromCol.items)
    }
    // A mensagem é a que o servidor devolveu, não um erro genérico.
    toast.error(getErrorMessage(e, t('occurrences.stageChangeFailed')))
  } finally {
    pending.value.delete(occ.id)
  }
}

onMounted(async () => {
  if (store.stages.length === 0) await store.fetchStages()
  await loadAll()
  unsubscribeOccurrenceChanged = wsService.onOccurrenceChanged(handleOccurrenceChanged)
})

onUnmounted(() => {
  unsubscribeOccurrenceChanged?.()
})

// O quadro espalha uma única busca em N requisições (uma por coluna), então um
// watch sem debounce multiplica cada tecla digitada pelo número de etapas.
// 300ms para bater com o debounce que useSearchPagination já usa na lista.
watchDebounced(() => props.protocol, loadAll, { debounce: 300 })
</script>

<template>
  <!-- Fills its container's height; each column scrolls on its own (same as
       the CRM board), so short columns don't stretch to the longest one. -->
  <div id="occurrences-board" class="flex h-full gap-3 overflow-x-auto p-4">
    <div
      v-for="col in columns"
      :key="col.stage.id"
      :data-board-column="col.stage.name"
      class="flex min-h-0 w-[288px] shrink-0 flex-col gap-2"
    >
      <!-- Stage band (Bitrix's coloured column header): a tint of the stage
           colour with a solid underline, so any stage colour stays legible
           in both themes. -->
      <div
        class="flex items-center justify-between gap-2 rounded-xl px-3 py-2.5"
        :style="{
          backgroundColor: `color-mix(in srgb, ${col.stage.color} 18%, hsl(var(--card)))`,
          boxShadow: `inset 0 -3px 0 ${col.stage.color}`,
        }"
      >
        <span class="flex min-w-0 items-center gap-2 text-[13px] font-semibold text-foreground">
          <span class="h-2.5 w-2.5 shrink-0 rounded-full" :style="{ backgroundColor: col.stage.color }" aria-hidden="true" />
          <span class="truncate">{{ col.stage.name }}</span>
        </span>
        <span
          class="min-w-[24px] rounded-full bg-card/80 px-2 py-0.5 text-center text-xs font-semibold text-foreground"
          data-board-column-count
        >{{ col.total }}</span>
      </div>

      <!-- The drop zone fills the column body (padding lives inside it and
           "load more" sits in its footer), so a drop anywhere in the column counts. -->
      <div class="flex min-h-24 flex-1 flex-col overflow-y-auto rounded-xl bg-muted/50">
        <div v-if="col.failed" class="p-5 text-center">
          <p class="text-xs text-muted-foreground">{{ $t('occurrences.columnLoadFailed') }}</p>
          <Button variant="outline" size="sm" class="mt-2" @click="loadColumn(col, 1)">
            {{ $t('common.retryLoad') }}
          </Button>
        </div>

        <template v-else>
          <draggable
            v-model="col.items"
            :group="{ name: 'occurrences' }"
            :move="canMove"
            item-key="id"
            data-board-dropzone
            class="flex min-h-16 flex-1 flex-col gap-2 p-2"
            @start="onDragStart(col, $event)"
            @change="onColumnChange(col, $event)"
            @end="dragOrigin = null"
          >
            <template #item="{ element, index }">
              <OccurrenceCard :style="{ order: cardOrder(index) }" :occurrence="element" :disabled="pending.has(element.id)" @click="goToDetail(element)" />
            </template>
            <!-- Inside the drop zone and centred (my-auto in a flex column), so an
                 empty column reads clearly and still takes a drop anywhere. -->
            <template #footer>
              <div v-if="col.loading" class="order-last my-auto flex justify-center p-3">
                <Spinner class="h-4 w-4" />
              </div>
              <p v-else-if="col.items.length === 0" class="order-last my-auto p-3 text-center text-xs text-muted-foreground">
                {{ $t('occurrences.columnEmpty') }}
              </p>
              <Button
                v-if="hasMore(col) && !col.loading"
                variant="ghost"
                size="sm"
                class="order-last shrink-0"
                @click="loadColumn(col, col.page + 1)"
              >
                {{ $t('occurrences.loadMore') }}
              </Button>
            </template>
          </draggable>
        </template>
      </div>
    </div>
  </div>
</template>
