<script setup lang="ts">
// Side panel of one document: its content and the chunks it is searched by. The write actions
// (edit, archive/reactivate, reindex, delete) exist only with knowledge:write; the list below
// is re-read by the parent after any change (reloadToken).
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { knowledgeService, type KnowledgeChunk, type KnowledgeDocument, type KnowledgeScopes } from '@/services/api'
import { getErrorMessage, } from '@/lib/api-utils'
import { formatDateTime } from '@/lib/utils'
import { classifyError, isManual, isStaleChunk, scopeKind, scopeName, whoArchived } from '@/lib/knowledge'
import { Loader2, Pencil, Archive, ArchiveRestore, RefreshCw, Trash2 } from 'lucide-vue-next'

const props = defineProps<{
  docId: string | null
  canWrite: boolean
  scopes: KnowledgeScopes | null
  indexVersion?: number // only who administers knows it
  reloadToken: number
}>()
const emit = defineEmits<{
  close: []
  gone: []
  edit: [id: string]
  toggleArchive: [id: string]
  remove: [doc: KnowledgeDocument]
  reindexed: []
}>()

const { t } = useI18n()

const doc = ref<KnowledgeDocument | null>(null)
const chunks = ref<KnowledgeChunk[]>([])
const loading = ref(false)
const failed = ref(false)
const reindexing = ref(false)
const tab = ref('content')
let request = 0

const open = computed({
  get: () => !!props.docId,
  set: (v: boolean) => { if (!v) emit('close') },
})

const manual = computed(() => !!doc.value && isManual(doc.value))
const archivedBy = computed(() => (doc.value ? whoArchived(doc.value) : 'none'))
const scopeText = computed(() => {
  if (!doc.value) return ''
  const units = props.scopes?.units ?? []
  const depts = props.scopes?.departments ?? []
  switch (scopeKind(doc.value)) {
    case 'unit': return `${t('knowledge.unit')}: ${scopeName(units, doc.value.unit_id)}`
    case 'department': return `${t('knowledge.department')}: ${scopeName(depts, doc.value.department_id)}`
    case 'unitDepartment': return `${scopeName(units, doc.value.unit_id)} / ${scopeName(depts, doc.value.department_id)}`
    default: return t('knowledge.scopeOrganization')
  }
})

async function load() {
  if (!props.docId) return
  const mine = ++request
  loading.value = true
  failed.value = false
  try {
    const [d, c] = await Promise.all([knowledgeService.get(props.docId), knowledgeService.chunks(props.docId)])
    if (mine !== request) return
    doc.value = d.data.data
    chunks.value = c.data.data.chunks
  } catch (e) {
    if (mine !== request) return
    if (classifyError(e) === 'notFound') {
      toast.error(t('knowledge.gone')) // deleted, archived for a reader, or out of reach: close
      emit('gone')
    } else {
      failed.value = true
      toast.error(getErrorMessage(e, t('knowledge.loadDocumentFailed')))
    }
  } finally {
    if (mine === request) loading.value = false
  }
}

watch(() => [props.docId, props.reloadToken], () => {
  if (props.docId) {
    tab.value = 'content'
    load()
  } else {
    doc.value = null
    chunks.value = []
  }
}, { immediate: true })

async function reindex() {
  if (!props.docId) return
  reindexing.value = true
  try {
    const res = await knowledgeService.reindexDocument(props.docId)
    toast.success(t('knowledge.reindexedDocument', { count: res.data.data.chunks }))
    emit('reindexed')
    await load()
  } catch (e) {
    if (classifyError(e) === 'notFound') {
      toast.error(t('knowledge.gone'))
      emit('gone')
    } else {
      toast.error(getErrorMessage(e, t('knowledge.reindexFailed')))
    }
  } finally {
    reindexing.value = false
  }
}
</script>

<template>
  <Sheet v-model:open="open">
    <SheetContent side="right" class="w-full sm:max-w-2xl overflow-y-auto" data-testid="knowledge-panel">
      <SheetHeader>
        <SheetTitle class="pr-6 break-words">{{ doc?.title ?? t('common.loading') }}</SheetTitle>
        <SheetDescription>{{ t('knowledge.panelDesc') }}</SheetDescription>
      </SheetHeader>

      <div v-if="loading && !doc" class="flex items-center justify-center py-16 text-muted-foreground">
        <Loader2 class="h-5 w-5 animate-spin mr-2" />{{ t('common.loading') }}
      </div>
      <Alert v-else-if="failed && !doc" variant="destructive" class="mt-4">
        <AlertDescription class="space-y-2">
          <p>{{ t('knowledge.loadDocumentFailed') }}</p>
          <Button size="sm" variant="outline" @click="load">{{ t('common.retryLoad') }}</Button>
        </AlertDescription>
      </Alert>

      <div v-else-if="doc" class="mt-4 space-y-4">
        <div class="flex flex-wrap items-center gap-2 text-xs">
          <Badge variant="outline">{{ t(`knowledge.types.${doc.source_type}`) }}</Badge>
          <Badge :variant="doc.status === 'active' ? 'default' : 'secondary'">{{ t(`knowledge.status.${doc.status}`) }}</Badge>
          <Badge v-if="archivedBy === 'import'" variant="outline">{{ t('knowledge.archivedByImport') }}</Badge>
          <Badge v-else-if="archivedBy === 'user'" variant="outline">{{ t('knowledge.archivedByUser') }}</Badge>
          <span class="text-muted-foreground">{{ scopeText }}</span>
          <span class="text-muted-foreground">· {{ formatDateTime(doc.updated_at) }}</span>
        </div>

        <div v-if="canWrite" class="flex flex-wrap gap-2" data-testid="knowledge-panel-actions">
          <Button size="sm" variant="outline" @click="emit('edit', doc.id)"><Pencil class="h-4 w-4 mr-2" />{{ t('common.edit') }}</Button>
          <Button size="sm" variant="outline" @click="emit('toggleArchive', doc.id)">
            <component :is="doc.status === 'active' ? Archive : ArchiveRestore" class="h-4 w-4 mr-2" />
            {{ doc.status === 'active' ? t('knowledge.archive') : t('knowledge.reactivate') }}
          </Button>
          <Button size="sm" variant="outline" :disabled="reindexing" @click="reindex">
            <Loader2 v-if="reindexing" class="h-4 w-4 mr-2 animate-spin" /><RefreshCw v-else class="h-4 w-4 mr-2" />
            {{ t('knowledge.reindexDocument') }}
          </Button>
          <Button size="sm" variant="outline" class="text-destructive" @click="emit('remove', doc)"><Trash2 class="h-4 w-4 mr-2" />{{ t('common.delete') }}</Button>
        </div>

        <Tabs v-model="tab">
          <TabsList>
            <TabsTrigger value="content">{{ t('knowledge.tabContent') }}</TabsTrigger>
            <TabsTrigger value="chunks">{{ t('knowledge.tabChunks') }} ({{ chunks.length }})</TabsTrigger>
          </TabsList>

          <TabsContent value="content" class="space-y-3">
            <Alert v-if="manual"><AlertDescription>{{ t('knowledge.manualReadOnly') }}</AlertDescription></Alert>
            <p v-if="doc.origin" class="text-xs text-muted-foreground break-all">{{ t('knowledge.origin') }}: {{ doc.origin }}</p>
            <pre class="whitespace-pre-wrap break-words rounded-md border p-3 text-sm max-h-[60vh] overflow-y-auto font-sans" data-testid="knowledge-body">{{ doc.body }}</pre>
          </TabsContent>

          <TabsContent value="chunks" class="space-y-2" data-testid="knowledge-chunks">
            <p v-if="chunks.length === 0" class="text-sm text-muted-foreground">{{ t('knowledge.noChunks') }}</p>
            <details v-for="c in chunks" :key="c.id" class="rounded-md border p-3 text-sm" :open="chunks.length <= 3">
              <summary class="cursor-pointer flex flex-wrap items-center gap-2">
                <span class="font-medium">#{{ c.chunk_index }}</span>
                <span class="text-muted-foreground break-words">{{ c.heading }}</span>
                <span class="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
                  {{ t('knowledge.chars', { count: c.char_count }) }}
                  <Badge v-if="isStaleChunk(c.index_version, indexVersion)" variant="destructive">{{ t('knowledge.stale') }} v{{ c.index_version }}</Badge>
                  <Badge v-else variant="outline">v{{ c.index_version }}</Badge>
                </span>
              </summary>
              <pre class="mt-2 whitespace-pre-wrap break-words font-sans">{{ c.content }}</pre>
            </details>
          </TabsContent>
        </Tabs>
      </div>
    </SheetContent>
  </Sheet>
</template>
