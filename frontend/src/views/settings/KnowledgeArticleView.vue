<script setup lang="ts">
// Article reader (Docy-style): the category's documents on the left, the
// article in the middle, "On this page" on the right with the current section
// highlighted while scrolling. The body is plain text turned into blocks by
// parseKnowledgeBody — rendered with regular elements, never v-html.
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { knowledgeService, type KnowledgeDocument } from '@/services/api'
import { useAuthStore } from '@/stores/auth'
import { knowledgeCapabilities } from '@/lib/knowledge'
import { parseKnowledgeBody, readingMinutes } from '@/lib/knowledge-reader'
import { formatDate } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { ErrorState } from '@/components/shared'
import { ChevronRight, ChevronLeft, Clock, CalendarDays, Settings2, ListTree, ArrowUp } from 'lucide-vue-next'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const canWrite = computed(() => knowledgeCapabilities((r, a) => authStore.hasPermission(r, a)).canWrite)

const doc = ref<KnowledgeDocument | null>(null)
const siblings = ref<KnowledgeDocument[]>([])
const loading = ref(true)
const failed = ref(false)

const parsed = computed(() => parseKnowledgeBody(doc.value?.body ?? ''))
const minutes = computed(() => readingMinutes(doc.value?.body ?? ''))
const index = computed(() => siblings.value.findIndex(d => d.id === doc.value?.id))
const prev = computed(() => (index.value > 0 ? siblings.value[index.value - 1] : null))
const next = computed(() => (index.value >= 0 && index.value < siblings.value.length - 1 ? siblings.value[index.value + 1] : null))

const scroller = ref<HTMLElement | null>(null)
const activeId = ref<string | null>(null)

async function load(id: string) {
  loading.value = true
  failed.value = false
  try {
    const res = await knowledgeService.get(id)
    doc.value = res.data.data
    // The category's documents (alphabetical, like the list) for the side nav and prev/next.
    const list = await knowledgeService.list({
      source_type: doc.value.source_type,
      limit: 100,
      status: canWrite.value ? 'active' : undefined,
    })
    // Natural order, so numbered manuals read 1, 1.1, 2 … 10 instead of 1, 10, 11, 2.
    siblings.value = [...list.data.data.documents].sort((a, b) =>
      a.title.localeCompare(b.title, undefined, { numeric: true, sensitivity: 'base' }))
  } catch {
    failed.value = true
    doc.value = null
  } finally {
    loading.value = false
  }
  await nextTick()
  const hash = decodeURIComponent(route.hash.replace(/^#/, ''))
  document.querySelector('[data-reader-nav] [aria-current="page"]')?.scrollIntoView({ block: 'nearest' })
  if (hash) jumpTo(hash, false)
  else scroller.value?.scrollTo({ top: 0 })
  updateActive()
}

// Scroll-spy: the last heading that has passed the top band of the reader.
function updateActive() {
  const root = scroller.value
  if (!root) return
  const top = root.getBoundingClientRect().top + 96
  let current: string | null = parsed.value.toc[0]?.id ?? null
  // At the very end the last headings can never reach the top band: the last one wins.
  if (root.scrollTop + root.clientHeight >= root.scrollHeight - 4) {
    activeId.value = parsed.value.toc.at(-1)?.id ?? null
    return
  }
  for (const entry of parsed.value.toc) {
    const el = document.getElementById(entry.id)
    if (el && el.getBoundingClientRect().top <= top) current = entry.id
  }
  activeId.value = current
}

function jumpTo(id: string, smooth = true) {
  const el = document.getElementById(id)
  if (!el) return
  el.scrollIntoView({ behavior: smooth ? 'smooth' : 'auto', block: 'start' })
  activeId.value = id
  router.replace({ hash: `#${id}` })
}

function toTop() {
  scroller.value?.scrollTo({ top: 0, behavior: 'smooth' })
}

watch(() => route.params.id, (id) => { if (typeof id === 'string') load(id) })
onMounted(() => {
  if (typeof route.params.id === 'string') load(route.params.id)
  scroller.value?.addEventListener('scroll', updateActive, { passive: true })
})
onBeforeUnmount(() => scroller.value?.removeEventListener('scroll', updateActive))
</script>

<template>
  <div ref="scroller" class="h-full overflow-y-auto bg-muted/40" data-testid="knowledge-article">
    <div v-if="loading && !doc" class="flex h-full items-center justify-center"><Spinner class="h-6 w-6" /></div>

    <ErrorState
      v-else-if="failed"
      :title="t('common.loadErrorTitle')"
      :description="t('common.loadErrorDescription')"
      :retry-label="t('common.retryLoad')"
      class="h-full"
      @retry="load(String(route.params.id))"
    />

    <template v-else-if="doc">
      <!-- Breadcrumb band -->
      <div class="border-b bg-card">
        <div class="mx-auto flex max-w-[1240px] flex-wrap items-center justify-between gap-3 px-4 py-3 sm:px-6">
          <nav class="flex min-w-0 items-center gap-1.5 text-[13px] text-muted-foreground" :aria-label="t('knowledge.readerBreadcrumb')">
            <RouterLink to="/knowledge" class="shrink-0 hover:text-foreground">{{ t('knowledge.tabHome') }}</RouterLink>
            <ChevronRight class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <RouterLink :to="{ path: '/knowledge', query: { type: doc.source_type } }" class="shrink-0 hover:text-foreground">
              {{ t(`knowledge.types.${doc.source_type}`) }}
            </RouterLink>
            <ChevronRight class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <span class="truncate text-foreground" aria-current="page">{{ doc.title }}</span>
          </nav>
          <Button v-if="canWrite" variant="outline" size="sm" @click="router.push({ path: '/knowledge', query: { doc: doc.id } })">
            <Settings2 class="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />{{ t('knowledge.readerManage') }}
          </Button>
        </div>
      </div>

      <div class="mx-auto grid max-w-[1240px] gap-8 px-4 py-8 sm:px-6 lg:grid-cols-[230px_minmax(0,1fr)] xl:grid-cols-[230px_minmax(0,1fr)_220px]">
        <!-- Category navigation -->
        <aside class="hidden lg:block">
          <div class="sticky top-6">
            <p class="mb-2 px-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
              {{ t('knowledge.readerInCategory', { category: t(`knowledge.types.${doc.source_type}`) }) }}
            </p>
            <nav data-reader-nav class="max-h-[calc(100vh-14rem)] space-y-0.5 overflow-y-auto pr-1" :aria-label="t(`knowledge.types.${doc.source_type}`)">
              <RouterLink
                v-for="d in siblings"
                :key="d.id"
                :to="`/knowledge/${d.id}`"
                class="block rounded-lg px-2 py-1.5 text-[13px] leading-snug transition-colors"
                :class="d.id === doc.id ? 'bg-primary/10 font-medium text-foreground' : 'text-muted-foreground hover:bg-card hover:text-foreground'"
                :aria-current="d.id === doc.id ? 'page' : undefined"
              >
                {{ d.title }}
              </RouterLink>
            </nav>
          </div>
        </aside>

        <!-- Article -->
        <article class="min-w-0 rounded-2xl bg-card px-6 py-8 shadow-sm ring-1 ring-black/5 sm:px-10 dark:ring-white/10">
          <header class="mb-8 border-b pb-6">
            <span class="mb-3 inline-flex rounded-full bg-primary/10 px-2.5 py-0.5 text-xs font-medium text-primary">
              {{ t(`knowledge.types.${doc.source_type}`) }}
            </span>
            <h1 class="text-[28px] font-bold leading-tight tracking-tight text-foreground">{{ doc.title }}</h1>
            <div class="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-[13px] text-muted-foreground">
              <span class="inline-flex items-center gap-1.5"><CalendarDays class="h-3.5 w-3.5" aria-hidden="true" />{{ t('knowledge.readerUpdated', { date: formatDate(doc.updated_at) }) }}</span>
              <span class="inline-flex items-center gap-1.5"><Clock class="h-3.5 w-3.5" aria-hidden="true" />{{ t('knowledge.readerMinutes', { n: minutes }) }}</span>
            </div>
          </header>

          <!-- On narrow screens the table of contents sits above the text -->
          <details v-if="parsed.toc.length" class="mb-8 rounded-xl border bg-muted/40 xl:hidden">
            <summary class="flex cursor-pointer items-center gap-2 px-4 py-3 text-sm font-medium">
              <ListTree class="h-4 w-4 text-muted-foreground" aria-hidden="true" />{{ t('knowledge.readerOnThisPage') }}
            </summary>
            <ul class="space-y-1 px-4 pb-3">
              <li v-for="e in parsed.toc" :key="e.id" :class="e.level === 3 && 'pl-3'">
                <a :href="`#${e.id}`" class="text-[13px] text-muted-foreground hover:text-foreground" @click.prevent="jumpTo(e.id)">{{ e.text }}</a>
              </li>
            </ul>
          </details>

          <div class="reader-body">
            <template v-for="(b, i) in parsed.blocks" :key="i">
              <h2 v-if="b.kind === 'heading' && b.level === 2" :id="b.id">{{ b.text }}</h2>
              <h3 v-else-if="b.kind === 'heading'" :id="b.id">{{ b.text }}</h3>
              <p v-else-if="b.kind === 'paragraph'">
                <template v-for="(line, j) in b.lines" :key="j"><br v-if="j > 0">{{ line }}</template>
              </p>
              <ol v-else-if="b.kind === 'list' && b.ordered"><li v-for="(it, j) in b.items" :key="j">{{ it }}</li></ol>
              <ul v-else-if="b.kind === 'list'"><li v-for="(it, j) in b.items" :key="j">{{ it }}</li></ul>
              <div v-else-if="b.kind === 'table'" class="reader-table">
                <table>
                  <thead><tr><th v-for="(c, j) in b.header" :key="j">{{ c }}</th></tr></thead>
                  <tbody><tr v-for="(r, j) in b.rows" :key="j"><td v-for="(c, k) in r" :key="k">{{ c }}</td></tr></tbody>
                </table>
              </div>
            </template>
          </div>

          <!-- Previous / next inside the category -->
          <nav v-if="prev || next" class="mt-12 grid gap-3 border-t pt-6 sm:grid-cols-2" :aria-label="t('knowledge.readerPager')">
            <RouterLink
              v-if="prev"
              :to="`/knowledge/${prev.id}`"
              class="group rounded-xl border p-4 transition-colors hover:border-primary/40 hover:bg-muted/40"
            >
              <span class="flex items-center gap-1 text-xs text-muted-foreground"><ChevronLeft class="h-3.5 w-3.5" aria-hidden="true" />{{ t('knowledge.readerPrev') }}</span>
              <span class="mt-1 block truncate text-sm font-medium text-foreground">{{ prev.title }}</span>
            </RouterLink>
            <span v-else class="hidden sm:block" />
            <RouterLink
              v-if="next"
              :to="`/knowledge/${next.id}`"
              class="group rounded-xl border p-4 text-right transition-colors hover:border-primary/40 hover:bg-muted/40"
            >
              <span class="flex items-center justify-end gap-1 text-xs text-muted-foreground">{{ t('knowledge.readerNext') }}<ChevronRight class="h-3.5 w-3.5" aria-hidden="true" /></span>
              <span class="mt-1 block truncate text-sm font-medium text-foreground">{{ next.title }}</span>
            </RouterLink>
          </nav>
        </article>

        <!-- On this page -->
        <aside v-if="parsed.toc.length" class="hidden xl:block">
          <div class="sticky top-6">
            <p class="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
              <ListTree class="h-3.5 w-3.5" aria-hidden="true" />{{ t('knowledge.readerOnThisPage') }}
            </p>
            <ul class="space-y-0.5 border-l">
              <li v-for="e in parsed.toc" :key="e.id">
                <a
                  :href="`#${e.id}`"
                  class="-ml-px block border-l-2 py-1 text-[13px] leading-snug transition-colors"
                  :class="[
                    e.level === 3 ? 'pl-6' : 'pl-3',
                    activeId === e.id ? 'border-primary font-medium text-foreground' : 'border-transparent text-muted-foreground hover:text-foreground',
                  ]"
                  :aria-current="activeId === e.id ? 'location' : undefined"
                  @click.prevent="jumpTo(e.id)"
                >{{ e.text }}</a>
              </li>
            </ul>
            <button type="button" class="mt-4 flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground" @click="toTop">
              <ArrowUp class="h-3.5 w-3.5" aria-hidden="true" />{{ t('knowledge.readerTop') }}
            </button>
          </div>
        </aside>
      </div>
    </template>
  </div>
</template>

<style scoped>
/* Long-form reading: comfortable measure and rhythm, headings that anchor
   below the top edge when jumped to. */
.reader-body {
  max-width: 72ch;
  font-size: 15px;
  line-height: 1.75;
  color: hsl(var(--foreground) / 0.88);
}
.reader-body > * + * { margin-top: 1em; }
.reader-body h2,
.reader-body h3 {
  scroll-margin-top: 1.5rem;
  color: hsl(var(--foreground));
  font-weight: 700;
  line-height: 1.3;
}
.reader-body h2 { margin-top: 2.2em; font-size: 1.35rem; }
.reader-body h3 { margin-top: 1.8em; font-size: 1.1rem; }
.reader-body ul,
.reader-body ol { padding-left: 1.4em; }
.reader-body ul { list-style: disc; }
.reader-body ol { list-style: decimal; }
.reader-body li + li { margin-top: 0.35em; }
.reader-table {
  overflow-x: auto;
  border: 1px solid hsl(var(--border));
  border-radius: 0.75rem;
}
.reader-table table { width: 100%; border-collapse: collapse; font-size: 14px; line-height: 1.5; }
.reader-table th {
  background: hsl(var(--muted));
  text-align: left;
  font-weight: 600;
  color: hsl(var(--foreground));
}
.reader-table th,
.reader-table td { padding: 0.6rem 0.85rem; vertical-align: top; border-bottom: 1px solid hsl(var(--border)); }
.reader-table tbody tr:last-child td { border-bottom: 0; }
</style>
