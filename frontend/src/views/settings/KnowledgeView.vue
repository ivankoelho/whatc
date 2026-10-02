<script setup lang="ts">
// Administration of the knowledge base (Fase 8B-2). Reading needs knowledge:read; every write
// control (create, edit, archive, reactivate, delete, reindex, index status) exists only with
// knowledge:write. The unit/department selectors follow /knowledge/scopes: who may choose a
// context gets them (labelled "Scope" for administrators, "Context" otherwise); a plain reader
// gets none and the server uses their own context. Nothing here infers a context.
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Collapsible, CollapsibleContent } from '@/components/ui/collapsible'
import { PageHeader, DataTable, DeleteConfirmDialog, ConfirmDialog, IconButton, ErrorState, SearchInput, type Column } from '@/components/shared'
import KnowledgeDocumentDialog from '@/components/knowledge/KnowledgeDocumentDialog.vue'
import KnowledgeDocumentPanel from '@/components/knowledge/KnowledgeDocumentPanel.vue'
import KnowledgeSearchPanel from '@/components/knowledge/KnowledgeSearchPanel.vue'
import KnowledgeIndexBar from '@/components/knowledge/KnowledgeIndexBar.vue'
import {
  knowledgeService,
  type KnowledgeDocument,
  type KnowledgeIndexStatus,
  type KnowledgeScopes,
} from '@/services/api'
import { useMediaQuery } from '@vueuse/core'
import { useAuthStore } from '@/stores/auth'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDate } from '@/lib/utils'
import {
  buildUpdatePayload,
  classifyError,
  formFromDocument,
  isManual,
  knowledgeCapabilities,
  pickerOptions,
  scopeKind,
  scopeName,
  whoArchived,
} from '@/lib/knowledge'
import { BookOpen, Plus, Pencil, Trash2, Archive, ArchiveRestore, Search, X } from 'lucide-vue-next'

const { t } = useI18n()
const authStore = useAuthStore()
const ALL = '__all'

const caps = computed(() => knowledgeCapabilities((r, a) => authStore.hasPermission(r, a)))
const canWrite = computed(() => caps.value.canWrite)

const scopes = ref<KnowledgeScopes | null>(null)
const status = ref<KnowledgeIndexStatus | null>(null)
const docs = ref<KnowledgeDocument[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const loading = ref(true)
const error = ref(false)

const search = ref('')
const typeFilter = ref(ALL)
const statusFilter = ref(ALL)
const unitFilter = ref(ALL)
const departmentFilter = ref(ALL)
const searchOpen = ref(false)

const selectedId = ref<string | null>(null)
const reloadToken = ref(0)
const dialogOpen = ref(false)
const dialogDocId = ref<string | null>(null)
const deleteTarget = ref<KnowledgeDocument | null>(null)
const deleteOpen = ref(false)
const deleting = ref(false)
const reindexBusy = ref(false)
const reindexAllOpen = ref(false)

const canChoose = computed(() => !!scopes.value?.can_choose_context)
const filterLabel = computed(() => (canWrite.value ? t('knowledge.filterScope') : t('knowledge.filterContext')))
const units = computed(() => pickerOptions(scopes.value?.units ?? [], unitFilter.value === ALL ? null : unitFilter.value))
const departments = computed(() => pickerOptions(scopes.value?.departments ?? [], departmentFilter.value === ALL ? null : departmentFilter.value))
const hasFilters = computed(() =>
  !!search.value.trim() || typeFilter.value !== ALL || statusFilter.value !== ALL || unitFilter.value !== ALL || departmentFilter.value !== ALL)
const showCliHint = computed(() => docs.value.some(isManual) || (!loading.value && total.value === 0 && !hasFilters.value))

// On a phone the table keeps the columns that fit (scope and date are in the side panel).
const narrow = useMediaQuery('(max-width: 640px)')
const columns = computed<Column<KnowledgeDocument>[]>(() => [
  { key: 'title', label: t('knowledge.colTitle') },
  { key: 'source_type', label: t('knowledge.colType') },
  ...(narrow.value ? [] : [{ key: 'scope', label: t('knowledge.colScope') }]),
  { key: 'status', label: t('knowledge.colStatus') },
  ...(narrow.value ? [] : [{ key: 'updated_at', label: t('knowledge.colUpdated') }]),
  { key: 'actions', label: t('common.actions'), align: 'right' as const },
])

function scopeText(d: KnowledgeDocument): string {
  const u = scopeName(scopes.value?.units ?? [], d.unit_id)
  const p = scopeName(scopes.value?.departments ?? [], d.department_id)
  switch (scopeKind(d)) {
    case 'unit': return `${t('knowledge.unit')}: ${u}`
    case 'department': return `${t('knowledge.department')}: ${p}`
    case 'unitDepartment': return `${u} / ${p}`
    default: return t('knowledge.scopeOrganization')
  }
}

let listRequest = 0
async function loadDocs() {
  const mine = ++listRequest // an older answer never overwrites a newer one
  loading.value = true
  try {
    const res = await knowledgeService.list({
      page: page.value,
      limit: pageSize.value,
      search: search.value.trim() || undefined,
      source_type: typeFilter.value === ALL ? undefined : typeFilter.value,
      status: canWrite.value && statusFilter.value !== ALL ? statusFilter.value : undefined,
      unit_id: canChoose.value && unitFilter.value !== ALL ? unitFilter.value : undefined,
      department_id: canChoose.value && departmentFilter.value !== ALL ? departmentFilter.value : undefined,
    })
    if (mine !== listRequest) return
    docs.value = res.data.data.documents ?? []
    total.value = res.data.data.total
    error.value = false
  } catch (e) {
    if (mine !== listRequest) return
    error.value = true
    docs.value = []
    toast.error(getErrorMessage(e, t('knowledge.loadFailed')))
  } finally {
    if (mine === listRequest) loading.value = false
  }
}

async function loadScopes() {
  try {
    scopes.value = (await knowledgeService.scopes()).data.data
  } catch (e) {
    scopes.value = null
    toast.error(getErrorMessage(e, t('knowledge.scopesFailed')))
  }
}

async function loadStatus() {
  if (!canWrite.value) return // the status endpoint needs knowledge:write: never called without it
  try {
    status.value = (await knowledgeService.status()).data.data
  } catch {
    status.value = null
  }
}

async function refresh() {
  await Promise.all([loadDocs(), loadStatus()])
  reloadToken.value++
}

let timer: ReturnType<typeof setTimeout> | undefined
watch(search, () => {
  clearTimeout(timer)
  timer = setTimeout(() => { page.value = 1; loadDocs() }, 300)
})
watch([typeFilter, statusFilter, unitFilter, departmentFilter, pageSize], () => { page.value = 1; loadDocs() })
onBeforeUnmount(() => clearTimeout(timer))

function onPage(p: number) { page.value = p; loadDocs() }
function clearFilters() {
  search.value = ''
  typeFilter.value = statusFilter.value = unitFilter.value = departmentFilter.value = ALL
}

onMounted(async () => {
  await loadScopes()
  await Promise.all([loadDocs(), loadStatus()])
})

function openCreate() { dialogDocId.value = null; dialogOpen.value = true }
function openEdit(id: string) { dialogDocId.value = id; dialogOpen.value = true }

async function onSaved(doc: KnowledgeDocument) {
  await refresh()
  selectedId.value = doc.id
}

async function onGone() {
  selectedId.value = null
  await refresh()
}

// Archive / reactivate: read the document, then PUT the complete representation with only the status
// changed and the version just read as expected_updated_at (a manual_html sends scope and status only).
async function toggleArchive(id: string) {
  try {
    const d = (await knowledgeService.get(id)).data.data
    const next = d.status === 'active' ? 'archived' : 'active'
    await knowledgeService.update(id, buildUpdatePayload(d, { ...formFromDocument(d), status: next }, d.updated_at))
    toast.success(t(next === 'archived' ? 'knowledge.archived' : 'knowledge.reactivated'))
    await refresh()
  } catch (e) {
    switch (classifyError(e)) {
      case 'conflict': toast.error(t('knowledge.conflictToast')); await refresh(); break
      case 'notFound': toast.error(t('knowledge.gone')); await onGone(); break
      case 'forbidden': toast.error(t('knowledge.forbidden')); break
      default: toast.error(getErrorMessage(e, t('knowledge.saveFailed')))
    }
  }
}

function askDelete(d: KnowledgeDocument) { deleteTarget.value = d; deleteOpen.value = true }

async function doDelete() {
  const d = deleteTarget.value
  if (!d) return
  deleting.value = true
  try {
    await knowledgeService.remove(d.id)
    toast.success(t('knowledge.deleted'))
  } catch (e) {
    const kind = classifyError(e)
    if (kind !== 'notFound') { // 404 = already gone: treated as success
      toast.error(kind === 'forbidden' ? t('knowledge.forbidden') : getErrorMessage(e, t('knowledge.deleteFailed')))
      deleting.value = false
      return
    }
  }
  deleting.value = false
  deleteOpen.value = false
  if (selectedId.value === d.id) selectedId.value = null
  await refresh()
}

async function reindex(onlyStale: boolean) {
  if (!onlyStale && !reindexAllOpen.value) { reindexAllOpen.value = true; return }
  reindexAllOpen.value = false
  reindexBusy.value = true
  try {
    const r = (await knowledgeService.reindex(onlyStale)).data.data
    toast.success(t('knowledge.reindexResult', { documents: r.documents, chunks: r.chunks, skipped: r.skipped, remaining: r.stale_remaining }))
    await refresh()
  } catch (e) {
    toast.error(classifyError(e) === 'forbidden' ? t('knowledge.forbidden') : getErrorMessage(e, t('knowledge.reindexFailed')))
  } finally {
    reindexBusy.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="t('knowledge.title')" :icon="BookOpen" icon-gradient="bg-gradient-to-br from-emerald-500 to-teal-600 shadow-emerald-500/20" back-link="/settings">
      <template #actions>
        <Button variant="outline" size="sm" data-testid="knowledge-search-toggle" :aria-label="t('knowledge.searchTool')" @click="searchOpen = !searchOpen">
          <Search class="h-4 w-4 sm:mr-2" /><span class="hidden sm:inline">{{ t('knowledge.searchTool') }}</span>
        </Button>
        <Button v-if="canWrite" size="sm" data-testid="knowledge-create" @click="openCreate">
          <Plus class="h-4 w-4 mr-2" />{{ t('knowledge.create') }}
        </Button>
      </template>
    </PageHeader>

    <ErrorState
      v-if="error && !loading"
      :title="t('common.loadErrorTitle')"
      :description="t('common.loadErrorDescription')"
      :retry-label="t('common.retryLoad')"
      class="flex-1"
      @retry="refresh"
    />

    <ScrollArea v-else class="flex-1">
      <div class="p-6">
        <div class="max-w-6xl mx-auto space-y-4">
          <KnowledgeIndexBar v-if="canWrite" :status="status" :busy="reindexBusy" @reindex="reindex" />

          <Alert v-if="showCliHint" data-testid="knowledge-cli-hint">
            <AlertDescription>
              {{ t('knowledge.cliHint') }} <code class="rounded bg-muted px-1 py-0.5 text-xs">whatomate knowledge import-manuals -org &lt;id&gt;</code>
            </AlertDescription>
          </Alert>

          <Collapsible :open="searchOpen">
            <CollapsibleContent>
              <Card>
                <CardHeader>
                  <CardTitle>{{ t('knowledge.searchTool') }}</CardTitle>
                  <CardDescription>{{ t('knowledge.searchToolDesc') }}</CardDescription>
                </CardHeader>
                <CardContent>
                  <KnowledgeSearchPanel :scopes="scopes" @open-document="(id) => selectedId = id" />
                </CardContent>
              </Card>
            </CollapsibleContent>
          </Collapsible>

          <Card>
            <CardHeader>
              <div class="flex items-center justify-between flex-wrap gap-4">
                <div>
                  <CardTitle>{{ t('knowledge.documents') }}</CardTitle>
                  <CardDescription>{{ t('knowledge.documentsDesc') }}</CardDescription>
                </div>
                <SearchInput v-model="search" :placeholder="t('knowledge.searchTitles')" class="w-64" />
              </div>
              <div class="flex flex-wrap items-end gap-3 pt-2" data-testid="knowledge-filters">
                <Select v-model="typeFilter">
                  <SelectTrigger class="w-44"><SelectValue :placeholder="t('knowledge.colType')" /></SelectTrigger>
                  <SelectContent>
                    <SelectItem :value="ALL">{{ t('knowledge.allTypes') }}</SelectItem>
                    <SelectItem v-for="ty in ['faq', 'article', 'process', 'text', 'markdown', 'manual_html']" :key="ty" :value="ty">{{ t(`knowledge.types.${ty}`) }}</SelectItem>
                  </SelectContent>
                </Select>
                <Select v-if="canWrite" v-model="statusFilter">
                  <SelectTrigger class="w-44"><SelectValue :placeholder="t('knowledge.colStatus')" /></SelectTrigger>
                  <SelectContent>
                    <SelectItem :value="ALL">{{ t('knowledge.allStatuses') }}</SelectItem>
                    <SelectItem value="active">{{ t('knowledge.status.active') }}</SelectItem>
                    <SelectItem value="archived">{{ t('knowledge.status.archived') }}</SelectItem>
                  </SelectContent>
                </Select>
                <template v-if="canChoose">
                  <Select v-model="unitFilter">
                    <SelectTrigger class="w-52" data-testid="knowledge-filter-unit"><SelectValue :placeholder="`${filterLabel}: ${t('knowledge.unit')}`" /></SelectTrigger>
                    <SelectContent>
                      <SelectItem :value="ALL">{{ filterLabel }}: {{ t('knowledge.anyUnit') }}</SelectItem>
                      <SelectItem v-for="u in units" :key="u.id" :value="u.id">{{ u.active ? u.name : `${u.name} (${t('knowledge.inactive')})` }}</SelectItem>
                    </SelectContent>
                  </Select>
                  <Select v-model="departmentFilter">
                    <SelectTrigger class="w-52"><SelectValue :placeholder="`${filterLabel}: ${t('knowledge.department')}`" /></SelectTrigger>
                    <SelectContent>
                      <SelectItem :value="ALL">{{ filterLabel }}: {{ t('knowledge.anyDepartment') }}</SelectItem>
                      <SelectItem v-for="d in departments" :key="d.id" :value="d.id">{{ d.active ? d.name : `${d.name} (${t('knowledge.inactive')})` }}</SelectItem>
                    </SelectContent>
                  </Select>
                </template>
                <Select :model-value="String(pageSize)" @update:model-value="(v) => pageSize = Number(v)">
                  <SelectTrigger class="w-32"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="20">20 / {{ t('knowledge.perPage') }}</SelectItem>
                    <SelectItem value="50">50 / {{ t('knowledge.perPage') }}</SelectItem>
                    <SelectItem value="100">100 / {{ t('knowledge.perPage') }}</SelectItem>
                  </SelectContent>
                </Select>
                <Button v-if="hasFilters" variant="ghost" size="sm" @click="clearFilters"><X class="h-4 w-4 mr-1" />{{ t('knowledge.clearFilters') }}</Button>
              </div>
            </CardHeader>
            <CardContent>
              <DataTable
                :items="docs"
                :columns="columns"
                :is-loading="loading"
                :empty-icon="BookOpen"
                :empty-title="hasFilters ? t('knowledge.noMatching') : t('knowledge.noDocuments')"
                :empty-description="hasFilters ? t('knowledge.noMatchingDesc') : t('knowledge.noDocumentsDesc')"
                server-pagination
                :current-page="page"
                :total-items="total"
                :page-size="pageSize"
                item-name="documents"
                @page-change="onPage"
              >
                <template #cell-title="{ item }">
                  <button type="button" class="text-left font-medium hover:underline break-words" data-testid="knowledge-row-title" @click="selectedId = item.id">
                    {{ item.title }}
                  </button>
                </template>
                <template #cell-source_type="{ item }">
                  <Badge :variant="isManual(item) ? 'secondary' : 'outline'">{{ t(`knowledge.types.${item.source_type}`) }}</Badge>
                </template>
                <template #cell-scope="{ item }">
                  <span class="text-muted-foreground">{{ scopeText(item) }}</span>
                </template>
                <template #cell-status="{ item }">
                  <div class="flex flex-wrap items-center gap-1">
                    <Badge :variant="item.status === 'active' ? 'default' : 'secondary'">{{ t(`knowledge.status.${item.status}`) }}</Badge>
                    <span v-if="whoArchived(item) === 'import'" class="text-xs text-muted-foreground">{{ t('knowledge.archivedByImport') }}</span>
                    <span v-else-if="whoArchived(item) === 'user'" class="text-xs text-muted-foreground">{{ t('knowledge.archivedByUser') }}</span>
                  </div>
                </template>
                <template #cell-updated_at="{ item }">
                  <span class="text-muted-foreground">{{ formatDate(item.updated_at) }}</span>
                </template>
                <template #cell-actions="{ item }">
                  <div v-if="canWrite" class="flex items-center justify-end gap-1">
                    <IconButton :icon="Pencil" :label="t('common.edit')" class="h-8 w-8" @click="openEdit(item.id)" />
                    <IconButton
                      :icon="item.status === 'active' ? Archive : ArchiveRestore"
                      :label="item.status === 'active' ? t('knowledge.archive') : t('knowledge.reactivate')"
                      class="h-8 w-8"
                      @click="toggleArchive(item.id)"
                    />
                    <IconButton :label="t('common.delete')" class="h-8 w-8" @click="askDelete(item)">
                      <Trash2 class="h-4 w-4 text-destructive" />
                    </IconButton>
                  </div>
                </template>
                <template v-if="canWrite && !hasFilters" #empty-action>
                  <Button variant="outline" size="sm" @click="openCreate"><Plus class="h-4 w-4 mr-2" />{{ t('knowledge.create') }}</Button>
                </template>
              </DataTable>
            </CardContent>
          </Card>
        </div>
      </div>
    </ScrollArea>

    <KnowledgeDocumentPanel
      :doc-id="selectedId"
      :can-write="canWrite"
      :scopes="scopes"
      :index-version="status?.index_version"
      :reload-token="reloadToken"
      @close="selectedId = null"
      @gone="onGone"
      @edit="openEdit"
      @toggle-archive="toggleArchive"
      @remove="askDelete"
      @reindexed="loadStatus"
    />

    <KnowledgeDocumentDialog v-if="canWrite" v-model:open="dialogOpen" :doc-id="dialogDocId" :scopes="scopes" @saved="onSaved" @gone="onGone" />

    <DeleteConfirmDialog
      v-model:open="deleteOpen"
      :title="t('knowledge.deleteTitle')"
      :description="deleteTarget && isManual(deleteTarget) ? t('knowledge.deleteManualDesc', { name: deleteTarget.title }) : t('knowledge.deleteDesc', { name: deleteTarget?.title ?? '' })"
      :is-submitting="deleting"
      @confirm="doDelete"
    />

    <ConfirmDialog
      v-model:open="reindexAllOpen"
      :title="t('knowledge.reindexAllTitle')"
      :description="t('knowledge.reindexAllDesc')"
      :confirm-label="t('knowledge.reindexAll')"
      :cancel-label="t('common.cancel')"
      @confirm="reindex(false)"
    />
  </div>
</template>
