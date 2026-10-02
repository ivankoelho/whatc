<script setup lang="ts">
// Administrative search tool of the knowledge base (strict mode). It is NOT the chatbot's retrieval.
// The unit/department selectors exist only for who may choose a context (can_choose_context) and
// start EMPTY (= no unit/department = global content only); the screen never fills them in.
// Someone who may not choose sends no context at all: the server uses their own.
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { knowledgeService, type KnowledgeHit, type KnowledgeScopes } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { classifyError, formatScore, pickerOptions, scopeName } from '@/lib/knowledge'
import { Loader2, Search } from 'lucide-vue-next'

const props = defineProps<{ scopes: KnowledgeScopes | null }>()
const emit = defineEmits<{ openDocument: [id: string] }>()

const { t } = useI18n()
const NONE = '__none'

const q = ref('')
const limit = ref('10')
const unitId = ref<string | null>(null)
const departmentId = ref<string | null>(null)
const searching = ref(false)
const searched = ref(false)
const hits = ref<KnowledgeHit[]>([])
const error = ref('')
const outOfReach = ref(false)

const canChoose = computed(() => !!props.scopes?.can_choose_context)
const units = computed(() => pickerOptions(props.scopes?.units ?? [], unitId.value))
const departments = computed(() => pickerOptions(props.scopes?.departments ?? [], departmentId.value))
const ownText = computed(() => {
  const s = props.scopes
  if (!s || (!s.own.unit_id && !s.own.department_id)) return t('knowledge.contextGlobalOnly')
  return [scopeName(s.units, s.own.unit_id), scopeName(s.departments, s.own.department_id)].filter(Boolean).join(' / ')
})

async function run() {
  const text = q.value.trim()
  if (!text) return
  searching.value = true
  error.value = ''
  outOfReach.value = false
  try {
    const res = await knowledgeService.search({
      q: text,
      limit: Number(limit.value),
      ...(canChoose.value ? { unit_id: unitId.value ?? undefined, department_id: departmentId.value ?? undefined } : {}),
    })
    hits.value = res.data.data.results ?? []
    searched.value = true
  } catch (e) {
    hits.value = []
    searched.value = false
    if (classifyError(e) === 'forbidden') outOfReach.value = true
    else error.value = getErrorMessage(e, t('knowledge.searchFailed'))
  } finally {
    searching.value = false
  }
}

function snippet(text: string): string {
  return text.length > 320 ? `${text.slice(0, 320).trimEnd()}…` : text
}
</script>

<template>
  <div class="space-y-3" data-testid="knowledge-search">
    <form class="flex flex-wrap items-end gap-3" @submit.prevent="run">
      <div class="space-y-1 flex-1 min-w-[14rem]">
        <Label for="ks-q">{{ t('knowledge.searchQuery') }}</Label>
        <Input id="ks-q" v-model="q" :placeholder="t('knowledge.searchPh')" />
      </div>
      <template v-if="canChoose">
        <div class="space-y-1 w-48">
          <Label>{{ t('knowledge.contextUnit') }}</Label>
          <Select :model-value="unitId ?? NONE" @update:model-value="(v) => unitId = v === NONE ? null : String(v)">
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem :value="NONE">{{ t('knowledge.noUnit') }}</SelectItem>
              <SelectItem v-for="u in units" :key="u.id" :value="u.id">{{ u.active ? u.name : `${u.name} (${t('knowledge.inactive')})` }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="space-y-1 w-48">
          <Label>{{ t('knowledge.contextDepartment') }}</Label>
          <Select :model-value="departmentId ?? NONE" @update:model-value="(v) => departmentId = v === NONE ? null : String(v)">
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem :value="NONE">{{ t('knowledge.noDepartment') }}</SelectItem>
              <SelectItem v-for="d in departments" :key="d.id" :value="d.id">{{ d.active ? d.name : `${d.name} (${t('knowledge.inactive')})` }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </template>
      <div class="space-y-1 w-24">
        <Label>{{ t('knowledge.searchLimit') }}</Label>
        <Select v-model="limit">
          <SelectTrigger><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="5">5</SelectItem>
            <SelectItem value="10">10</SelectItem>
            <SelectItem value="20">20</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <Button type="submit" :disabled="!q.trim() || searching">
        <Loader2 v-if="searching" class="h-4 w-4 mr-2 animate-spin" /><Search v-else class="h-4 w-4 mr-2" />
        {{ t('knowledge.searchRun') }}
      </Button>
    </form>

    <p v-if="!canChoose" class="text-xs text-muted-foreground" data-testid="knowledge-fixed-context">
      {{ t('knowledge.contextFixed', { context: ownText }) }}
    </p>
    <p v-else class="text-xs text-muted-foreground">{{ t('knowledge.contextChooseHelp') }}</p>

    <Alert v-if="outOfReach" variant="destructive"><AlertDescription>{{ t('knowledge.contextOutOfReach') }}</AlertDescription></Alert>
    <Alert v-else-if="error" variant="destructive"><AlertDescription>{{ error }}</AlertDescription></Alert>

    <p v-if="!searched && !error && !outOfReach && !searching" class="text-sm text-muted-foreground">{{ t('knowledge.searchInitial') }}</p>
    <p v-else-if="searched && hits.length === 0" class="text-sm text-muted-foreground" data-testid="knowledge-no-results">
      {{ t('knowledge.searchEmpty') }} {{ t('knowledge.searchEmptyHint') }}
    </p>

    <ul v-if="hits.length" class="space-y-2" data-testid="knowledge-results">
      <li v-for="h in hits" :key="h.chunk_id" class="rounded-md border p-3 text-sm">
        <div class="flex flex-wrap items-center gap-2">
          <button type="button" class="font-medium text-left hover:underline" @click="emit('openDocument', h.document_id)">{{ h.title }}</button>
          <span v-if="h.heading && h.heading !== h.title" class="text-muted-foreground">— {{ h.heading }}</span>
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger as-child>
                <Badge variant="outline" class="ml-auto" data-testid="knowledge-score">{{ t('knowledge.score') }} {{ formatScore(h.score) }}</Badge>
              </TooltipTrigger>
              <TooltipContent class="max-w-xs">{{ t('knowledge.scoreHelp') }}</TooltipContent>
            </Tooltip>
          </TooltipProvider>
        </div>
        <p class="mt-1 whitespace-pre-wrap break-words text-muted-foreground">{{ snippet(h.content) }}</p>
        <p class="mt-1 text-xs text-muted-foreground break-all">
          {{ t(`knowledge.types.${h.citation.source_type}`) }}<template v-if="h.citation.origin"> · {{ h.citation.origin }}</template>
        </p>
      </li>
    </ul>
  </div>
</template>
