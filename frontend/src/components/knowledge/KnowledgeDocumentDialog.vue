<script setup lang="ts">
// Create / edit a knowledge document. The body is a plain Textarea with a counter (limit 200,000
// characters) and an optional upload of a text/Markdown file (read in the browser, never uploaded).
// PUT is the complete representation: both scope keys are always sent. expected_updated_at is the
// version read when the dialog opened, so a concurrent change answers 409 and nothing is overwritten.
// manual_html: title/body/type are read-only (the importer owns them); only scope and status change.
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Button } from '@/components/ui/button'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { CrudFormDialog } from '@/components/shared'
import { knowledgeService, type KnowledgeDocument, type KnowledgeScopes } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import {
  EDITABLE_SOURCE_TYPES,
  KNOWLEDGE_MAX_BODY,
  buildCreatePayload,
  buildUpdatePayload,
  charCount,
  classifyError,
  emptyKnowledgeForm,
  formFromDocument,
  isManual,
  pickerOptions,
  readKnowledgeFile,
  sourceTypeForFile,
  validateKnowledgeForm,
  type EditableSourceType,
  type KnowledgeForm,
} from '@/lib/knowledge'
import { Loader2, Upload, AlertTriangle } from 'lucide-vue-next'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ docId: string | null; scopes: KnowledgeScopes | null }>()
const emit = defineEmits<{ saved: [doc: KnowledgeDocument]; gone: [] }>()

const { t } = useI18n()

const NONE = '__none'
const form = ref<KnowledgeForm>(emptyKnowledgeForm())
const loaded = ref<KnowledgeDocument | null>(null) // the document as read (edit mode)
const loading = ref(false)
const saving = ref(false)
const conflict = ref(false)
const serverError = ref('')
const fieldErrors = ref<string[]>([])
const fileInput = ref<HTMLInputElement | null>(null)

const isEditing = computed(() => !!props.docId)
const manual = computed(() => !!loaded.value && isManual(loaded.value))
const count = computed(() => charCount(form.value.body))
const overLimit = computed(() => count.value > KNOWLEDGE_MAX_BODY)
const units = computed(() => props.scopes?.units ?? [])
const departments = computed(() => props.scopes?.departments ?? [])
const unitOptions = computed(() => pickerOptions(units.value, form.value.unit_id))
const departmentOptions = computed(() => pickerOptions(departments.value, form.value.department_id))

function reset() {
  form.value = emptyKnowledgeForm()
  loaded.value = null
  conflict.value = false
  serverError.value = ''
  fieldErrors.value = []
}

async function load() {
  if (!props.docId) return
  loading.value = true
  try {
    const res = await knowledgeService.get(props.docId)
    loaded.value = res.data.data
    form.value = formFromDocument(res.data.data)
    conflict.value = false
    serverError.value = ''
  } catch (e) {
    if (classifyError(e) === 'notFound') {
      toast.error(t('knowledge.gone'))
      open.value = false
      emit('gone')
    } else {
      toast.error(getErrorMessage(e, t('knowledge.loadDocumentFailed')))
      open.value = false
    }
  } finally {
    loading.value = false
  }
}

watch(open, (isOpen) => {
  if (!isOpen) return
  reset()
  if (props.docId) load()
})

// After a failed submit the messages follow the fields (they go away as soon as the field is fixed).
watch(() => [form.value.title, form.value.body, form.value.source_type], () => {
  if (fieldErrors.value.length) fieldErrors.value = validateKnowledgeForm(form.value)
})

function setUnit(v: unknown) { form.value.unit_id = v === NONE ? null : String(v) }
function setDepartment(v: unknown) { form.value.department_id = v === NONE ? null : String(v) }
function setType(v: unknown) { form.value.source_type = String(v) as EditableSourceType }
function setStatus(v: unknown) { form.value.status = v === 'archived' ? 'archived' : 'active' }

function optionLabel(item: { name: string; active: boolean }) {
  return item.active ? item.name : `${item.name} (${t('knowledge.inactive')})`
}

async function onFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  const res = await readKnowledgeFile(file)
  if (!res.ok) {
    toast.error(res.reason === 'type' ? t('knowledge.uploadType') : t('knowledge.uploadSize', { max: KNOWLEDGE_MAX_BODY.toLocaleString() }))
    return
  }
  form.value.body = res.text
  if (!form.value.title.trim()) form.value.title = file.name.replace(/\.[^.]+$/, '')
  form.value.source_type = sourceTypeForFile(file.name)
}

async function submit() {
  serverError.value = ''
  if (!manual.value) {
    fieldErrors.value = validateKnowledgeForm(form.value)
    if (fieldErrors.value.length) return
  }
  saving.value = true
  try {
    const res = isEditing.value && loaded.value
      ? await knowledgeService.update(props.docId as string, buildUpdatePayload(loaded.value, form.value, loaded.value.updated_at))
      : await knowledgeService.create(buildCreatePayload(form.value))
    toast.success(t(isEditing.value ? 'knowledge.updated' : 'knowledge.created'))
    open.value = false
    emit('saved', res.data.data)
  } catch (e) {
    switch (classifyError(e)) {
      case 'conflict':
        conflict.value = true // do not overwrite: the user reloads the current version first
        break
      case 'notFound':
        toast.error(t('knowledge.gone'))
        open.value = false
        emit('gone')
        break
      case 'forbidden':
        toast.error(t('knowledge.forbidden'))
        break
      default:
        serverError.value = getErrorMessage(e, t('knowledge.saveFailed'))
    }
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <CrudFormDialog
    v-model:open="open"
    :is-editing="isEditing"
    :is-submitting="saving"
    :hide-submit="conflict || loading"
    :edit-title="t('knowledge.editTitle')"
    :create-title="t('knowledge.createTitle')"
    :edit-description="manual ? t('knowledge.editManualDesc') : t('knowledge.editDesc')"
    :create-description="t('knowledge.createDesc')"
    max-width="max-w-3xl"
    :cancel-label="t('common.cancel')"
    :create-submit-label="t('common.create')"
    :edit-submit-label="t('common.update')"
    @submit="submit"
  >
    <div v-if="loading" class="flex items-center justify-center py-12 text-muted-foreground">
      <Loader2 class="h-5 w-5 animate-spin mr-2" />{{ t('common.loading') }}
    </div>

    <div v-else class="space-y-4">
      <Alert v-if="conflict" variant="destructive" data-testid="knowledge-conflict">
        <AlertTriangle class="h-4 w-4" />
        <AlertDescription class="space-y-2">
          <p>{{ t('knowledge.conflict') }}</p>
          <Button type="button" size="sm" variant="outline" @click="load">{{ t('knowledge.reloadCurrent') }}</Button>
        </AlertDescription>
      </Alert>
      <Alert v-if="manual">
        <AlertDescription>{{ t('knowledge.manualReadOnly') }}</AlertDescription>
      </Alert>
      <Alert v-if="serverError" variant="destructive">
        <AlertDescription>{{ serverError }}</AlertDescription>
      </Alert>

      <div class="space-y-2">
        <Label for="k-title">{{ t('knowledge.fieldTitle') }}</Label>
        <Input id="k-title" v-model="form.title" :disabled="manual" :placeholder="t('knowledge.fieldTitlePh')" />
        <p v-if="fieldErrors.includes('titleRequired')" class="text-xs text-destructive">{{ t('knowledge.errTitleRequired') }}</p>
        <p v-if="fieldErrors.includes('titleTooLong')" class="text-xs text-destructive">{{ t('knowledge.errTitleTooLong') }}</p>
      </div>

      <div class="grid gap-4 sm:grid-cols-2">
        <div class="space-y-2">
          <Label>{{ t('knowledge.fieldType') }}</Label>
          <div v-if="manual" class="text-sm text-muted-foreground">{{ t('knowledge.types.manual_html') }}</div>
          <Select v-else :model-value="form.source_type" @update:model-value="setType">
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem v-for="ty in EDITABLE_SOURCE_TYPES" :key="ty" :value="ty">{{ t(`knowledge.types.${ty}`) }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="space-y-2">
          <Label>{{ t('knowledge.fieldStatus') }}</Label>
          <Select :model-value="form.status" @update:model-value="setStatus">
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="active">{{ t('knowledge.status.active') }}</SelectItem>
              <SelectItem value="archived">{{ t('knowledge.status.archived') }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <div class="space-y-2">
        <p class="text-sm font-medium">{{ t('knowledge.scopeLabel') }}</p>
        <p class="text-xs text-muted-foreground">{{ t('knowledge.scopeHelp') }}</p>
        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-2">
            <Label>{{ t('knowledge.unit') }}</Label>
            <Select :model-value="form.unit_id ?? NONE" @update:model-value="setUnit">
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem :value="NONE">{{ t('knowledge.noUnit') }}</SelectItem>
                <SelectItem v-for="u in unitOptions" :key="u.id" :value="u.id">{{ optionLabel(u) }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-2">
            <Label>{{ t('knowledge.department') }}</Label>
            <Select :model-value="form.department_id ?? NONE" @update:model-value="setDepartment">
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem :value="NONE">{{ t('knowledge.noDepartment') }}</SelectItem>
                <SelectItem v-for="d in departmentOptions" :key="d.id" :value="d.id">{{ optionLabel(d) }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
      </div>

      <div class="space-y-2">
        <div class="flex items-center justify-between gap-2">
          <Label for="k-body">{{ t('knowledge.fieldBody') }}</Label>
          <template v-if="!manual">
            <input ref="fileInput" type="file" class="hidden" accept=".txt,.md,.markdown,text/plain,text/markdown" data-testid="knowledge-file" @change="onFile" />
            <Button type="button" variant="outline" size="sm" @click="fileInput?.click()">
              <Upload class="h-4 w-4 mr-2" />{{ t('knowledge.upload') }}
            </Button>
          </template>
        </div>
        <Textarea id="k-body" v-model="form.body" :rows="14" :disabled="manual" class="font-mono text-sm" :placeholder="t('knowledge.fieldBodyPh')" />
        <div class="flex items-center justify-between text-xs">
          <span>
            <span v-if="fieldErrors.includes('bodyRequired')" class="text-destructive">{{ t('knowledge.errBodyRequired') }}</span>
            <span v-else-if="fieldErrors.includes('bodyTooLong') || overLimit" class="text-destructive">{{ t('knowledge.errBodyTooLong', { max: KNOWLEDGE_MAX_BODY.toLocaleString() }) }}</span>
          </span>
          <span :class="overLimit ? 'text-destructive' : 'text-muted-foreground'" data-testid="knowledge-counter">
            {{ count.toLocaleString() }} / {{ KNOWLEDGE_MAX_BODY.toLocaleString() }}
          </span>
        </div>
      </div>
    </div>
  </CrudFormDialog>
</template>
