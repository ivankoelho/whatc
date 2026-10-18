<script setup lang="ts">
// Help-center front of the knowledge base (Docy-style): one big search, the
// categories (document types) with their counts, and what changed lately.
// Reading only — every document opens in the article reader
// (/knowledge/:id); ?type=<source_type> opens straight on a category (the
// reader's breadcrumb links there). Managing documents stays in "Documentos".
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { useDebounceFn } from '@vueuse/core'
import { knowledgeService, type KnowledgeDocument, type KnowledgeSourceType } from '@/services/api'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { formatDate } from '@/lib/utils'
import {
  Search, HelpCircle, Newspaper, Workflow, FileText, FileCode2, BookOpenText, ArrowLeft, ChevronRight, Clock,
} from 'lucide-vue-next'
import type { Component } from 'vue'

const props = defineProps<{ canWrite: boolean }>()

const route = useRoute()
const router = useRouter()
const openDoc = (id: string) => router.push(`/knowledge/${id}`)

const { t } = useI18n()
const ALL = '__all'

// Each type is a "category"; the icon and tone make them scannable at a glance.
const TYPES: { key: KnowledgeSourceType; icon: Component; tone: string }[] = [
  { key: 'faq', icon: HelpCircle, tone: 'bg-sky-500/10 text-sky-600 dark:text-sky-300' },
  { key: 'manual_html', icon: BookOpenText, tone: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-300' },
  { key: 'process', icon: Workflow, tone: 'bg-amber-500/15 text-amber-600 dark:text-amber-300' },
  { key: 'article', icon: Newspaper, tone: 'bg-rose-500/10 text-rose-600 dark:text-rose-300' },
  { key: 'text', icon: FileText, tone: 'bg-indigo-500/10 text-indigo-600 dark:text-indigo-300' },
  { key: 'markdown', icon: FileCode2, tone: 'bg-teal-500/10 text-teal-600 dark:text-teal-300' },
]
const typeMeta = (key: string) => TYPES.find(x => x.key === key) ?? TYPES[4]

// Writers can see archived documents too; the help center shows only live ones.
const statusParam = computed(() => (props.canWrite ? 'active' : undefined))

const counts = ref<Record<string, number>>({})
const recent = ref<KnowledgeDocument[]>([])
const loadingHome = ref(true)

async function loadHome() {
  loadingHome.value = true
  try {
    const [recentRes, ...typeRes] = await Promise.all([
      knowledgeService.list({ limit: 6, sort: 'updated', status: statusParam.value }),
      ...TYPES.map(ty => knowledgeService.list({ limit: 1, source_type: ty.key, status: statusParam.value })),
    ])
    recent.value = recentRes.data.data.documents
    counts.value = Object.fromEntries(TYPES.map((ty, i) => [ty.key, typeRes[i].data.data.total]))
  } catch {
    recent.value = []
    counts.value = {}
  } finally {
    loadingHome.value = false
  }
}

const categories = computed(() => TYPES.filter(ty => (counts.value[ty.key] ?? 0) > 0))

// Results mode: a query and/or a category replaces the front page.
const query = ref('')
const typeFilter = ref(ALL)
const results = ref<KnowledgeDocument[]>([])
const resultsTotal = ref(0)
const resultsPage = ref(1)
const loadingResults = ref(false)
const inResults = computed(() => !!query.value.trim() || typeFilter.value !== ALL)

let request = 0
async function loadResults(page = 1) {
  const mine = ++request
  loadingResults.value = true
  try {
    const res = await knowledgeService.list({
      page,
      limit: 30,
      search: query.value.trim() || undefined,
      source_type: typeFilter.value === ALL ? undefined : typeFilter.value,
      status: statusParam.value,
    })
    if (mine !== request) return
    const { documents, total } = res.data.data
    results.value = page === 1 ? documents : [...results.value, ...documents]
    resultsTotal.value = total
    resultsPage.value = page
  } finally {
    if (mine === request) loadingResults.value = false
  }
}
const debounced = useDebounceFn(() => loadResults(1), 300)
watch([query, typeFilter], () => { if (inResults.value) debounced() })

function openCategory(key: string) {
  typeFilter.value = key
}
function backHome() {
  query.value = ''
  typeFilter.value = ALL
}

onMounted(() => {
  loadHome()
  const type = route.query.type
  if (typeof type === 'string' && TYPES.some(ty => ty.key === type)) typeFilter.value = type
})
defineExpose({ refresh: () => { loadHome(); if (inResults.value) loadResults(1) } })
</script>

<template>
  <div class="min-h-full bg-muted/40">
    <!-- Hero: the one question this page answers -->
    <section class="relative overflow-hidden bg-gradient-to-br from-emerald-600 via-emerald-700 to-teal-800 px-6 pb-28 pt-14 text-white">
      <!-- soft shapes, decorative only -->
      <div class="pointer-events-none absolute -left-16 -top-20 h-72 w-72 rounded-full bg-emerald-300/25 blur-2xl" aria-hidden="true" />
      <div class="pointer-events-none absolute -bottom-24 right-[-4rem] h-80 w-80 rounded-full bg-amber-300/30 blur-2xl" aria-hidden="true" />
      <div class="pointer-events-none absolute right-1/4 top-10 h-3 w-3 rounded-full bg-white/60" aria-hidden="true" />

      <div class="relative mx-auto max-w-2xl text-center">
        <h2 class="text-[28px] font-bold leading-tight tracking-tight sm:text-4xl">{{ t('knowledge.homeTitle') }}</h2>
        <p class="mt-2 text-sm text-white/80 sm:text-base">{{ t('knowledge.homeSubtitle') }}</p>

        <form class="mt-8 flex items-stretch overflow-hidden rounded-2xl bg-white shadow-xl shadow-black/20 dark:bg-[#141416]" role="search" @submit.prevent="loadResults(1)">
          <label class="flex min-w-0 flex-1 items-center gap-3 px-5">
            <Search class="h-5 w-5 shrink-0 text-gray-400" aria-hidden="true" />
            <span class="sr-only">{{ t('knowledge.homeSearchPlaceholder') }}</span>
            <input
              v-model="query"
              type="search"
              data-testid="knowledge-home-search"
              :placeholder="t('knowledge.homeSearchPlaceholder')"
              class="h-14 min-w-0 flex-1 bg-transparent text-[15px] text-gray-900 placeholder:text-gray-400 focus:outline-none dark:text-white"
            >
          </label>
          <Select v-model="typeFilter">
            <SelectTrigger
              class="h-14 w-44 shrink-0 rounded-none border-0 border-l px-4 border-gray-200 bg-gray-50 text-[13px] text-gray-700 focus:ring-0 dark:border-white/10 dark:bg-white/5 dark:text-white/80"
              :aria-label="t('knowledge.colType')"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem :value="ALL">{{ t('knowledge.allTypes') }}</SelectItem>
              <SelectItem v-for="ty in TYPES" :key="ty.key" :value="ty.key">{{ t(`knowledge.types.${ty.key}`) }}</SelectItem>
            </SelectContent>
          </Select>
        </form>

        <div v-if="recent.length && !inResults" class="mt-5 flex flex-wrap items-center justify-center gap-2 text-xs">
          <span class="text-white/75">{{ t('knowledge.homeSuggested') }}</span>
          <button
            v-for="d in recent.slice(0, 3)"
            :key="d.id"
            type="button"
            class="max-w-[14rem] truncate rounded-full bg-white/15 px-3 py-1 font-medium text-white ring-1 ring-white/20 transition hover:bg-white/25 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white"
            @click="openDoc(d.id)"
          >
            {{ d.title }}
          </button>
        </div>
      </div>
    </section>

    <div class="relative mx-auto -mt-16 max-w-5xl px-4 pb-10 sm:px-6">
      <!-- Results -->
      <!-- One spacing scale, in px (html is 14px, so rem lands off-grid): 28px
           sides everywhere, 20px header, 16px row rhythm. The header gets a
           full rule; rows are split by the text column's own top border, so
           the divider always starts where the text does. -->
      <section v-if="inResults" class="overflow-hidden rounded-2xl bg-card shadow-lg ring-1 ring-black/5 dark:ring-white/10" aria-live="polite">
        <header class="flex flex-wrap items-center justify-between gap-3 border-b px-[28px] py-[20px]">
          <div class="min-w-0">
            <h3 class="truncate text-base font-semibold text-foreground">
              <template v-if="query.trim()">{{ t('knowledge.homeResultsFor', { q: query.trim() }) }}</template>
              <template v-else>{{ t(`knowledge.types.${typeFilter}`) }}</template>
            </h3>
            <p class="mt-0.5 text-xs text-muted-foreground">{{ t('knowledge.homeResults', resultsTotal) }}</p>
          </div>
          <Button variant="outline" size="sm" @click="backHome">
            <ArrowLeft class="mr-1.5 h-4 w-4" aria-hidden="true" />{{ t('knowledge.homeBack') }}
          </Button>
        </header>
        <div v-if="loadingResults && results.length === 0" class="flex justify-center px-[28px] py-[48px]"><Spinner class="h-5 w-5" /></div>
        <p v-else-if="results.length === 0" class="px-[28px] py-[48px] text-center text-sm text-muted-foreground">{{ t('knowledge.noMatching') }}</p>
        <ul v-else class="py-[8px]">
          <li v-for="(d, i) in results" :key="d.id">
            <button
              type="button"
              class="group flex w-full items-stretch gap-[16px] px-[28px] text-left transition-colors hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
              @click="openDoc(d.id)"
            >
              <span class="flex shrink-0 items-center py-[16px]">
                <span class="flex h-[40px] w-[40px] items-center justify-center rounded-xl" :class="typeMeta(d.source_type).tone">
                  <component :is="typeMeta(d.source_type).icon" class="h-5 w-5" aria-hidden="true" />
                </span>
              </span>
              <span class="flex min-w-0 flex-1 items-center gap-[16px] py-[16px]" :class="i > 0 && 'border-t'">
                <span class="min-w-0 flex-1">
                  <span class="block truncate text-[15px] font-medium text-foreground">{{ d.title }}</span>
                  <span class="mt-[2px] block text-xs text-muted-foreground">{{ t(`knowledge.types.${d.source_type}`) }} · {{ formatDate(d.updated_at) }}</span>
                </span>
                <ChevronRight class="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
              </span>
            </button>
          </li>
        </ul>
        <div v-if="results.length < resultsTotal" class="border-t px-[28px] py-[16px] text-center">
          <Button variant="ghost" size="sm" :disabled="loadingResults" @click="loadResults(resultsPage + 1)">{{ t('knowledge.loadMore') }}</Button>
        </div>
      </section>

      <template v-else>
        <!-- Categories -->
        <section class="overflow-hidden rounded-2xl bg-card shadow-lg ring-1 ring-black/5 dark:ring-white/10">
          <h3 class="sr-only">{{ t('knowledge.homeCategories') }}</h3>
          <div v-if="loadingHome" class="flex justify-center p-10"><Spinner class="h-5 w-5" /></div>
          <p v-else-if="categories.length === 0" class="p-10 text-center text-sm text-muted-foreground">{{ t('knowledge.noDocuments') }}</p>
          <!-- columns follow the number of categories, so one or two never leave an empty row -->
          <div v-else class="grid grid-cols-1" :class="[categories.length >= 2 && 'sm:grid-cols-2', categories.length >= 3 && 'lg:grid-cols-3']">
            <button
              v-for="ty in categories"
              :key="ty.key"
              type="button"
              class="group flex items-center gap-[16px] border-b border-r p-[28px] text-left transition-colors hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
              @click="openCategory(ty.key)"
            >
              <span class="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl" :class="ty.tone">
                <component :is="ty.icon" class="h-6 w-6" aria-hidden="true" />
              </span>
              <span class="min-w-0">
                <span class="block text-[15px] font-semibold text-foreground">{{ t(`knowledge.types.${ty.key}`) }}</span>
                <span class="block text-xs text-muted-foreground">{{ t('knowledge.homeDocCount', counts[ty.key] ?? 0) }}</span>
              </span>
            </button>
          </div>
        </section>

        <!-- Recently updated -->
        <section v-if="recent.length" class="mt-10">
          <h3 class="mb-[16px] flex items-center gap-2 text-sm font-semibold text-foreground">
            <Clock class="h-4 w-4 text-muted-foreground" aria-hidden="true" />{{ t('knowledge.homeRecent') }}
          </h3>
          <div class="grid gap-[16px] sm:grid-cols-2">
            <button
              v-for="d in recent"
              :key="d.id"
              type="button"
              class="group flex items-center gap-[16px] rounded-xl bg-card px-[20px] py-[16px] text-left shadow-sm ring-1 ring-black/5 transition hover:shadow-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring dark:ring-white/10"
              @click="openDoc(d.id)"
            >
              <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg" :class="typeMeta(d.source_type).tone">
                <component :is="typeMeta(d.source_type).icon" class="h-[18px] w-[18px]" aria-hidden="true" />
              </span>
              <span class="min-w-0 flex-1">
                <span class="block truncate text-sm font-medium text-foreground">{{ d.title }}</span>
                <span class="block text-xs text-muted-foreground">{{ t(`knowledge.types.${d.source_type}`) }} · {{ formatDate(d.updated_at) }}</span>
              </span>
              <ChevronRight class="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
            </button>
          </div>
        </section>
      </template>
    </div>
  </div>
</template>
