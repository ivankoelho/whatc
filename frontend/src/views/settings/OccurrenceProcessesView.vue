<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { PageHeader, DataTable, CrudFormDialog, DeleteConfirmDialog, IconButton, ErrorState, type Column } from '@/components/shared'
import {
  occurrenceProcessesService,
  occurrenceCategoriesService,
  occurrenceWhatHappenedService,
  departmentsService,
  type OccurrenceProcess,
  type OccurrenceProcessMessage,
  type OccurrenceMessageStage,
  type OccurrenceCategory,
  type OccurrenceWhatHappened,
  type Department,
} from '@/services/api'
import { useCrudState } from '@/composables/useCrudState'
import { useAuthStore } from '@/stores/auth'
import { toast } from 'vue-sonner'
import { Plus, Workflow, Pencil, Trash2 } from 'lucide-vue-next'
import { getErrorMessage } from '@/lib/api-utils'

const { t } = useI18n()
const authStore = useAuthStore()

const canWrite = computed(() => authStore.hasPermission('occurrences.processes', 'write'))
const canDelete = computed(() => authStore.hasPermission('occurrences.processes', 'delete'))

// Fixed required-field keys — this MVP has no generic field-type builder,
// just the four fields the "Abrir protocolo" form already knows about.
const REQUIRED_FIELDS: { key: string; labelKey: string }[] = [
  { key: 'invoice_number', labelKey: 'occurrenceProcesses.fieldInvoiceNumber' },
  { key: 'product_description', labelKey: 'occurrenceProcesses.fieldProductDescription' },
  { key: 'purchase_date', labelKey: 'occurrenceProcesses.fieldPurchaseDate' },
  { key: 'sale_channel', labelKey: 'occurrenceProcesses.fieldSaleChannel' },
]

const MESSAGE_STAGES: OccurrenceMessageStage[] = ['registration', 'documents', 'follow_up', 'forwarding', 'closing']
const stageLabelKey: Record<OccurrenceMessageStage, string> = {
  registration: 'occurrenceProcesses.stageRegistration',
  documents: 'occurrenceProcesses.stageDocuments',
  follow_up: 'occurrenceProcesses.stageFollowUp',
  forwarding: 'occurrenceProcesses.stageForwarding',
  closing: 'occurrenceProcesses.stageClosing',
}

interface ProcessFormData {
  name: string
  description: string
  category_id: string
  what_happened_id: string
  department_id: string
  guidance: string
  restrictions: string
  evidenceChecklistText: string
  required_fields: string[]
  response_minutes: number | ''
  resolution_minutes: number | ''
  is_active: boolean
}

const defaultFormData: ProcessFormData = {
  name: '',
  description: '',
  category_id: '',
  what_happened_id: '',
  department_id: '',
  guidance: '',
  restrictions: '',
  evidenceChecklistText: '',
  required_fields: [],
  response_minutes: '',
  resolution_minutes: '',
  is_active: true,
}

const {
  items: processes, isLoading, isSubmitting, isDialogOpen, editingItem: editingProcess, deleteDialogOpen, itemToDelete: processToDelete,
  formData, openCreateDialog: baseOpenCreateDialog, openEditDialog: baseOpenEditDialog, openDeleteDialog, closeDialog: baseCloseDialog, closeDeleteDialog,
} = useCrudState<OccurrenceProcess, ProcessFormData>(defaultFormData)

const error = ref(false)
const formError = ref('')

const categories = ref<OccurrenceCategory[]>([])
const reasons = ref<OccurrenceWhatHappened[]>([])
const departments = ref<Department[]>([])

const columns = computed<Column<OccurrenceProcess>[]>(() => [
  { key: 'name', label: t('occurrenceProcesses.columnName') },
  { key: 'category', label: t('occurrenceProcesses.columnCategory') },
  { key: 'what_happened', label: t('occurrenceProcesses.columnReason') },
  { key: 'is_active', label: t('occurrenceProcesses.columnActive'), align: 'center' },
  { key: 'actions', label: t('common.actions'), align: 'right' },
])

async function fetchProcesses() {
  isLoading.value = true
  error.value = false
  try {
    const res = await occurrenceProcessesService.list()
    processes.value = res.data.data.processes
  } catch (e) {
    error.value = true
    toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('occurrenceProcesses.title') })))
  } finally {
    isLoading.value = false
  }
}

async function fetchLookups() {
  try {
    const [categoriesRes, reasonsRes, departmentsRes] = await Promise.all([
      occurrenceCategoriesService.list(),
      occurrenceWhatHappenedService.list(),
      departmentsService.list(),
    ])
    categories.value = categoriesRes.data.data.categories
    reasons.value = reasonsRes.data.data.reasons
    departments.value = departmentsRes.data.data.departments
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('occurrenceProcesses.title') })))
  }
}

onMounted(() => {
  fetchProcesses()
  fetchLookups()
})

// Per-stage message state, keyed by stage. Reset whenever the dialog opens.
type StageForm = { content: string; is_active: boolean; loading: boolean; saving: boolean }
function emptyStageForms(): Record<OccurrenceMessageStage, StageForm> {
  const forms = {} as Record<OccurrenceMessageStage, StageForm>
  for (const stage of MESSAGE_STAGES) {
    forms[stage] = { content: '', is_active: true, loading: false, saving: false }
  }
  return forms
}
const stageForms = ref(emptyStageForms())
const messagesLoading = ref(false)

async function fetchMessages(processId: string) {
  messagesLoading.value = true
  try {
    const res = await occurrenceProcessesService.listMessages(processId)
    const byStage = new Map(res.data.data.messages.map((m: OccurrenceProcessMessage) => [m.stage, m]))
    for (const stage of MESSAGE_STAGES) {
      const existing = byStage.get(stage)
      stageForms.value[stage] = {
        content: existing?.content ?? '',
        is_active: existing?.is_active ?? true,
        loading: false,
        saving: false,
      }
    }
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('occurrenceProcesses.messages') })))
  } finally {
    messagesLoading.value = false
  }
}

async function saveMessage(stage: OccurrenceMessageStage) {
  if (!editingProcess.value) return
  stageForms.value[stage].saving = true
  try {
    await occurrenceProcessesService.upsertMessage(editingProcess.value.id, stage, {
      content: stageForms.value[stage].content,
      is_active: stageForms.value[stage].is_active,
    })
    toast.success(t('common.updatedSuccess', { resource: t(stageLabelKey[stage]) }))
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedSave', { resource: t(stageLabelKey[stage]) })))
  } finally {
    stageForms.value[stage].saving = false
  }
}

function openCreateDialog() {
  formError.value = ''
  baseOpenCreateDialog()
}

function openEditDialog(process: OccurrenceProcess) {
  formError.value = ''
  stageForms.value = emptyStageForms()
  baseOpenEditDialog(process, (p) => ({
    name: p.name,
    description: p.description ?? '',
    category_id: p.category_id ?? '',
    what_happened_id: p.what_happened_id ?? '',
    department_id: p.department_id ?? '',
    guidance: p.guidance ?? '',
    restrictions: p.restrictions ?? '',
    evidenceChecklistText: (p.evidence_checklist ?? []).join('\n'),
    required_fields: [...(p.required_fields ?? [])],
    response_minutes: p.response_minutes ?? '',
    resolution_minutes: p.resolution_minutes ?? '',
    is_active: p.is_active,
  }))
  fetchMessages(process.id)
}

function closeDialog() {
  baseCloseDialog()
  formError.value = ''
}

function toggleRequiredField(key: string, checked: boolean | 'indeterminate') {
  const set = new Set(formData.value.required_fields)
  if (checked === true) set.add(key)
  else set.delete(key)
  formData.value.required_fields = [...set]
}

function buildPayload(): Partial<OccurrenceProcess> {
  return {
    name: formData.value.name.trim(),
    description: formData.value.description.trim(),
    category_id: formData.value.category_id || undefined,
    what_happened_id: formData.value.what_happened_id || undefined,
    department_id: formData.value.department_id || undefined,
    guidance: formData.value.guidance,
    restrictions: formData.value.restrictions,
    evidence_checklist: formData.value.evidenceChecklistText
      .split('\n')
      .map((line) => line.trim())
      .filter(Boolean),
    required_fields: formData.value.required_fields,
    response_minutes: formData.value.response_minutes === '' ? undefined : Number(formData.value.response_minutes),
    resolution_minutes: formData.value.resolution_minutes === '' ? undefined : Number(formData.value.resolution_minutes),
    is_active: formData.value.is_active,
  }
}

async function saveProcess() {
  if (!formData.value.name.trim()) {
    toast.error(t('occurrenceProcesses.nameRequired'))
    return
  }
  formError.value = ''
  isSubmitting.value = true
  try {
    const payload = buildPayload()
    if (editingProcess.value) {
      await occurrenceProcessesService.update(editingProcess.value.id, payload)
      toast.success(t('common.updatedSuccess', { resource: t('occurrenceProcesses.process') }))
    } else {
      await occurrenceProcessesService.create(payload)
      toast.success(t('common.createdSuccess', { resource: t('occurrenceProcesses.process') }))
    }
    closeDialog()
    await fetchProcesses()
  } catch (e) {
    if ((e as any)?.response?.status === 409) {
      formError.value = getErrorMessage(e, t('occurrenceProcesses.conflictDefault'))
    } else {
      toast.error(getErrorMessage(e, t('common.failedSave', { resource: t('occurrenceProcesses.process') })))
    }
  } finally {
    isSubmitting.value = false
  }
}

async function confirmDelete() {
  if (!processToDelete.value) return
  isSubmitting.value = true
  try {
    await occurrenceProcessesService.delete(processToDelete.value.id)
    toast.success(t('common.deletedSuccess', { resource: t('occurrenceProcesses.process') }))
    closeDeleteDialog()
    await fetchProcesses()
  } catch (e) {
    if ((e as any)?.response?.status === 409) {
      toast.error(t('occurrenceProcesses.deleteInUse'))
    } else {
      toast.error(getErrorMessage(e, t('common.failedDelete', { resource: t('occurrenceProcesses.process') })))
    }
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('occurrenceProcesses.title')" :description="$t('occurrenceProcesses.subtitle')" :icon="Workflow" icon-gradient="bg-gradient-to-br from-violet-500 to-purple-600 shadow-violet-500/20" back-link="/settings">
      <template #actions>
        <Button v-if="canWrite" variant="outline" size="sm" @click="openCreateDialog"><Plus class="h-4 w-4 mr-2" />{{ $t('occurrenceProcesses.addProcess') }}</Button>
      </template>
    </PageHeader>

    <ErrorState
      v-if="error && !isLoading"
      :title="$t('common.loadErrorTitle')"
      :description="$t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="fetchProcesses"
    />

    <ScrollArea v-else orientation="vertical" class="flex-1">
      <div class="p-6">
        <div class="max-w-5xl mx-auto">
          <Card>
            <CardHeader>
              <CardTitle>{{ $t('occurrenceProcesses.cardTitle') }}</CardTitle>
              <CardDescription>{{ $t('occurrenceProcesses.cardDesc') }}</CardDescription>
            </CardHeader>
            <CardContent>
              <DataTable
                :items="processes"
                :columns="columns"
                :is-loading="isLoading"
                :empty-icon="Workflow"
                :empty-title="$t('occurrenceProcesses.noProcessesYet')"
                :empty-description="$t('occurrenceProcesses.noProcessesYetDesc')"
                item-name="processes"
              >
                <template #cell-name="{ item: process }">
                  <span class="font-medium">{{ process.name }}</span>
                </template>
                <template #cell-category="{ item: process }">
                  <span class="text-muted-foreground">{{ process.category?.name ?? '—' }}</span>
                </template>
                <template #cell-what_happened="{ item: process }">
                  <span class="text-muted-foreground">{{ process.what_happened?.name ?? '—' }}</span>
                </template>
                <template #cell-is_active="{ item: process }">
                  <Badge :variant="process.is_active ? 'success' : 'secondary'">{{ process.is_active ? $t('common.yes') : $t('common.no') }}</Badge>
                </template>
                <template #cell-actions="{ item: process }">
                  <div class="flex items-center justify-end gap-1">
                    <IconButton :icon="Pencil" :label="$t('occurrenceProcesses.editProcess')" class="h-8 w-8" @click="openEditDialog(process)" />
                    <IconButton v-if="canDelete" :label="$t('occurrenceProcesses.deleteTitle')" class="h-8 w-8" @click="openDeleteDialog(process)">
                      <Trash2 class="h-4 w-4 text-destructive" />
                    </IconButton>
                  </div>
                </template>
                <template #empty-action>
                  <Button v-if="canWrite" variant="outline" size="sm" @click="openCreateDialog">
                    <Plus class="h-4 w-4 mr-2" />
                    {{ $t('occurrenceProcesses.addProcess') }}
                  </Button>
                </template>
              </DataTable>
            </CardContent>
          </Card>
        </div>
      </div>
    </ScrollArea>

    <CrudFormDialog
      v-model:open="isDialogOpen"
      :is-editing="!!editingProcess"
      :is-submitting="isSubmitting"
      :edit-title="$t('occurrenceProcesses.editTitle')"
      :create-title="$t('occurrenceProcesses.createTitle')"
      :edit-description="$t('occurrenceProcesses.editDesc')"
      :create-description="$t('occurrenceProcesses.createDesc')"
      max-width="max-w-3xl"
      :hide-submit="!canWrite"
      @submit="saveProcess"
    >
      <div class="space-y-4">
        <p v-if="formError" class="text-sm text-destructive bg-destructive/10 rounded-md px-3 py-2">{{ formError }}</p>

        <div class="grid grid-cols-2 gap-4">
          <div class="space-y-2 col-span-2">
            <Label>{{ $t('occurrenceProcesses.name') }} <span class="text-destructive">*</span></Label>
            <Input v-model="formData.name" :placeholder="$t('occurrenceProcesses.namePlaceholder')" :disabled="!canWrite" maxlength="150" />
          </div>
          <div class="space-y-2 col-span-2">
            <Label>{{ $t('occurrenceProcesses.description') }}</Label>
            <Textarea v-model="formData.description" :rows="2" :disabled="!canWrite" />
          </div>
          <div class="space-y-2">
            <Label>{{ $t('occurrenceProcesses.category') }}</Label>
            <Select v-model="formData.category_id" :disabled="!canWrite">
              <SelectTrigger><SelectValue :placeholder="$t('occurrenceProcesses.selectCategory')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="c in categories" :key="c.id" :value="c.id">{{ c.name }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-2">
            <Label>{{ $t('occurrenceProcesses.reason') }}</Label>
            <Select v-model="formData.what_happened_id" :disabled="!canWrite">
              <SelectTrigger><SelectValue :placeholder="$t('occurrenceProcesses.selectReason')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="r in reasons" :key="r.id" :value="r.id">{{ r.name }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-2 col-span-2">
            <Label>{{ $t('occurrenceProcesses.department') }}</Label>
            <Select v-model="formData.department_id" :disabled="!canWrite">
              <SelectTrigger><SelectValue :placeholder="$t('occurrenceProcesses.selectDepartment')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="d in departments" :key="d.id" :value="d.id">{{ d.name }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-2 col-span-2">
            <Label>{{ $t('occurrenceProcesses.guidance') }}</Label>
            <Textarea v-model="formData.guidance" :rows="3" :disabled="!canWrite" />
          </div>
          <div class="space-y-2 col-span-2">
            <Label>{{ $t('occurrenceProcesses.restrictions') }}</Label>
            <Textarea v-model="formData.restrictions" :rows="3" :disabled="!canWrite" />
          </div>
          <div class="space-y-2 col-span-2">
            <Label>{{ $t('occurrenceProcesses.evidenceChecklist') }}</Label>
            <Textarea v-model="formData.evidenceChecklistText" :rows="3" :placeholder="$t('occurrenceProcesses.evidenceChecklistPlaceholder')" :disabled="!canWrite" />
            <p class="text-xs text-muted-foreground">{{ $t('occurrenceProcesses.evidenceChecklistHint') }}</p>
          </div>
          <div class="space-y-2 col-span-2">
            <Label>{{ $t('occurrenceProcesses.requiredFields') }}</Label>
            <div class="grid grid-cols-2 gap-2 border rounded-lg p-3">
              <div v-for="field in REQUIRED_FIELDS" :key="field.key" class="flex items-center gap-2">
                <Checkbox
                  :id="`field-${field.key}`"
                  :checked="formData.required_fields.includes(field.key)"
                  @update:checked="(checked) => toggleRequiredField(field.key, checked)"
                  :disabled="!canWrite"
                />
                <Label :for="`field-${field.key}`" class="cursor-pointer font-normal">{{ $t(field.labelKey) }}</Label>
              </div>
            </div>
          </div>
          <div class="space-y-2">
            <Label>{{ $t('occurrenceProcesses.responseMinutesLabel') }}</Label>
            <Input type="number" v-model.number="formData.response_minutes" :min="0" :disabled="!canWrite" />
          </div>
          <div class="space-y-2">
            <Label>{{ $t('occurrenceProcesses.resolutionMinutesLabel') }}</Label>
            <Input type="number" v-model.number="formData.resolution_minutes" :min="0" :disabled="!canWrite" />
          </div>
          <div class="flex items-center justify-between gap-4 pt-2 col-span-2">
            <Label>{{ $t('occurrenceProcesses.active') }}</Label>
            <Switch :checked="formData.is_active" @update:checked="formData.is_active = $event" :disabled="!canWrite" />
          </div>
        </div>

        <div v-if="editingProcess" class="pt-4 border-t">
          <h3 class="text-sm font-medium mb-3">{{ $t('occurrenceProcesses.messages') }}</h3>
          <Tabs default-value="registration" class="w-full">
            <TabsList class="grid w-full grid-cols-5">
              <TabsTrigger v-for="stage in MESSAGE_STAGES" :key="stage" :value="stage">{{ $t(stageLabelKey[stage]) }}</TabsTrigger>
            </TabsList>
            <TabsContent v-for="stage in MESSAGE_STAGES" :key="stage" :value="stage" class="space-y-3 pt-3">
              <Textarea v-model="stageForms[stage].content" :rows="4" :disabled="!canWrite || messagesLoading" :placeholder="$t('occurrenceProcesses.messagePlaceholder')" />
              <p v-if="!stageForms[stage].content" class="text-xs text-muted-foreground">{{ $t('occurrenceProcesses.messageHint') }}</p>
              <div class="flex items-center justify-between">
                <div class="flex items-center gap-2">
                  <Switch :checked="stageForms[stage].is_active" @update:checked="stageForms[stage].is_active = $event" :disabled="!canWrite" />
                  <Label class="font-normal">{{ $t('occurrenceProcesses.active') }}</Label>
                </div>
                <Button v-if="canWrite" size="sm" variant="outline" :disabled="stageForms[stage].saving || messagesLoading" @click="saveMessage(stage)">
                  {{ $t('common.save') }}
                </Button>
              </div>
            </TabsContent>
          </Tabs>
        </div>
      </div>
    </CrudFormDialog>

    <DeleteConfirmDialog v-model:open="deleteDialogOpen" :title="$t('occurrenceProcesses.deleteTitle')" :item-name="processToDelete?.name" :is-submitting="isSubmitting" @confirm="confirmDelete">
      <p class="text-sm text-muted-foreground">{{ $t('occurrenceProcesses.deleteWarning') }}</p>
    </DeleteConfirmDialog>
  </div>
</template>
