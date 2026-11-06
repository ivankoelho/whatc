<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2, Lock, Search, StickyNote } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { occurrencesService, type OccurrenceEvent } from '@/services/api'
import { eventIcons, eventLabels, historyFilterTypes } from '@/lib/occurrence-events'
import { formatDateTime } from '@/lib/utils'

// The whole history of a case in a large window: newest first, loaded a page at a time while scrolling,
// filterable by type and searchable. The server decides what each person may see (messages to the
// customer are withheld from people who cannot read that conversation, and say so).
const props = defineProps<{ open: boolean; occurrenceId: string; protocol: string; title: string }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
const { t } = useI18n()

type HistoryEvent = OccurrenceEvent & { redacted?: boolean }
const PAGE = 30
const events = ref<HistoryEvent[]>([])
const cursor = ref('')
const hasMore = ref(false)
const loading = ref(false)
const failed = ref(false)
const search = ref('')
const selected = ref<string[]>([])
const sentinel = ref<HTMLElement | null>(null)
const scroller = ref<HTMLElement | null>(null)
let observer: IntersectionObserver | null = null
let timer: ReturnType<typeof setTimeout> | undefined
let seq = 0

async function load(reset: boolean) {
  if (loading.value && !reset) return
  const mine = ++seq
  loading.value = true
  failed.value = false
  if (reset) { events.value = []; cursor.value = ''; hasMore.value = false }
  try {
    const res = await occurrencesService.history(props.occurrenceId, {
      limit: PAGE, cursor: reset ? undefined : cursor.value || undefined,
      types: selected.value.length ? selected.value.join(',') : undefined, q: search.value.trim() || undefined
    })
    if (mine !== seq) return
    const data = ((res.data as { data?: unknown }).data ?? res.data) as { events: HistoryEvent[]; has_more: boolean; next_cursor: string }
    const known = new Set(events.value.map(e => e.id))
    events.value = [...events.value, ...data.events.filter(e => !known.has(e.id))]
    hasMore.value = data.has_more
    cursor.value = data.next_cursor
  } catch {
    if (mine === seq) failed.value = true
  } finally {
    if (mine === seq) loading.value = false
  }
}

function watchSentinel() {
  observer?.disconnect()
  if (!sentinel.value || typeof IntersectionObserver === 'undefined') return
  observer = new IntersectionObserver(entries => {
    if (entries.some(e => e.isIntersecting) && hasMore.value && !loading.value && !failed.value) void load(false)
  }, { root: scroller.value, rootMargin: '200px' })
  observer.observe(sentinel.value)
}

watch(() => props.open, async open => {
  if (open) {
    search.value = ''
    selected.value = []
    await load(true)
    await nextTick()
    watchSentinel()
  } else {
    observer?.disconnect()
  }
}, { immediate: true })
watch([selected, search], () => {
  clearTimeout(timer)
  timer = setTimeout(() => void load(true), 300)
}, { deep: true })
watch(sentinel, () => watchSentinel())
onBeforeUnmount(() => { observer?.disconnect(); clearTimeout(timer) })

const toggle = (type: string) => {
  selected.value = selected.value.includes(type) ? selected.value.filter(x => x !== type) : [...selected.value, type]
}
const empty = computed(() => !loading.value && !failed.value && events.value.length === 0)
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="flex h-[92vh] max-w-4xl flex-col gap-3 p-0 sm:max-w-4xl" data-testid="history-dialog">
      <DialogHeader class="border-b px-6 py-4">
        <DialogTitle class="font-mono text-base">{{ protocol }} <span class="font-sans font-normal text-muted-foreground">· {{ title }}</span></DialogTitle>
        <DialogDescription>{{ t('occurrences.history.subtitle') }}</DialogDescription>
      </DialogHeader>

      <div class="space-y-2 px-6">
        <div class="relative">
          <Search class="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" aria-hidden="true" />
          <Input v-model="search" class="h-9 pl-8" :placeholder="t('occurrences.history.search')" :aria-label="t('occurrences.history.search')" data-testid="history-search" />
        </div>
        <div class="flex flex-wrap gap-1.5" role="group" :aria-label="t('occurrences.history.filter')">
          <button
            v-for="ty in historyFilterTypes" :key="ty" type="button"
            class="rounded-full border px-2.5 py-1 text-xs transition-colors hover:bg-muted"
            :class="selected.includes(ty) ? 'border-primary bg-primary/10 font-medium' : 'text-muted-foreground'"
            :aria-pressed="selected.includes(ty)" :data-testid="`history-filter-${ty}`" @click="toggle(ty)"
          >{{ t(eventLabels[ty]) }}</button>
        </div>
      </div>

      <div ref="scroller" class="min-h-0 flex-1 overflow-y-auto px-6 pb-6" data-testid="history-scroller">
        <p v-if="empty" class="py-10 text-center text-sm text-muted-foreground" data-testid="history-empty">{{ t('occurrences.history.empty') }}</p>
        <ol class="space-y-4" data-testid="history-list">
          <li v-for="e in events" :key="e.id" class="flex gap-3 text-sm" :data-testid="`history-event-${e.id}`">
            <component :is="eventIcons[e.type] || StickyNote" class="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
            <div class="min-w-0 flex-1">
              <p class="flex flex-wrap items-baseline gap-x-2">
                <span class="font-medium">{{ t(eventLabels[e.type] || 'occurrences.eventUnknown') }}</span>
                <span v-if="e.created_by_name" class="text-xs text-muted-foreground">{{ e.created_by_name }}</span>
                <span class="text-xs text-muted-foreground">{{ formatDateTime(e.created_at) }}</span>
              </p>
              <p v-if="e.redacted" class="mt-0.5 flex items-center gap-1 text-xs italic text-muted-foreground">
                <Lock class="h-3 w-3" aria-hidden="true" />{{ t('occurrences.history.withheld') }}
              </p>
              <p v-else-if="e.type === 'customer_notification'" class="mt-0.5 text-muted-foreground">{{ t('occurrences.notice.' + e.content) }}</p>
              <p v-else-if="e.content" class="mt-0.5 whitespace-pre-wrap text-muted-foreground [overflow-wrap:anywhere]">{{ e.content }}</p>
            </div>
          </li>
        </ol>
        <div ref="sentinel" class="flex h-10 items-center justify-center" aria-hidden="true">
          <Loader2 v-if="loading" class="h-5 w-5 animate-spin text-muted-foreground" />
        </div>
        <div v-if="failed" class="flex flex-col items-center gap-2 py-4 text-sm" role="alert" data-testid="history-error">
          <p class="text-destructive">{{ t('occurrences.history.failed') }}</p>
          <Button size="sm" variant="outline" @click="load(events.length === 0)">{{ t('common.retry') }}</Button>
        </div>
        <p v-if="!loading && !hasMore && events.length > 0" class="pb-2 text-center text-xs text-muted-foreground">{{ t('occurrences.history.end') }}</p>
      </div>
    </DialogContent>
  </Dialog>
</template>
