<script setup lang="ts">
import { formatCivilDate } from '@/lib/utils'
import { computed, ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { IconButton, DeleteConfirmDialog } from '@/components/shared'
import OccurrenceDocumentsEditor from '@/components/crm/OccurrenceDocumentsEditor.vue'
import AttachmentPreviewDialog from '@/components/shared/AttachmentPreviewDialog.vue'
import { occurrenceDocumentsService, occurrenceFilesService, type OccurrenceDocument } from '@/services/api'
import { type DocumentDraft, newDocumentDraft, draftToInput, isBlankDraft, MAX_ATTACHMENT_BYTES, ATTACHMENT_ACCEPT } from '@/lib/occurrence-documents'
import { getErrorMessage } from '@/lib/api-utils'
import { FileText, Paperclip, Plus, Trash2, Loader2 } from 'lucide-vue-next'

// embedded: shown inside the single Attachments card (no frame, no title, no own add button).
const props = defineProps<{ occurrenceId: string; canWrite: boolean; embedded?: boolean }>()
const emit = defineEmits<{ saved: [] }>()
const { t } = useI18n()

const documents = ref<OccurrenceDocument[]>([])
const isLoading = ref(true)
const draft = ref<DocumentDraft[] | null>(null)
const isSaving = ref(false)
const busyId = ref<string | null>(null)
const docToDelete = ref<OccurrenceDocument | null>(null)

async function load() {
  try {
    const res = await occurrenceDocumentsService.list(props.occurrenceId)
    documents.value = res.data.data.documents
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrenceDocuments.loadFailed')))
  } finally {
    isLoading.value = false
  }
}
onMounted(load)

/** Opens the invoice/coupon form, already on the chosen kind (called by the Attachments card). */
function startDraft(type: 'nf' | 'cupom') {
  draft.value = [{ ...newDocumentDraft(), type }]
}
defineExpose({ startDraft })

function formatDate(value?: string) {
  // purchase_date comes back as a timestamp at local midnight; show the date only.
  return formatCivilDate(value)
}

async function saveDraft() {
  const d = draft.value?.[0]
  if (!d) return
  if (isBlankDraft(d)) {
    toast.error(t('occurrenceDocuments.validationEmpty'))
    return
  }
  isSaving.value = true
  try {
    const res = await occurrenceDocumentsService.create(props.occurrenceId, draftToInput(d))
    if (d.file) await occurrenceDocumentsService.uploadAttachment(props.occurrenceId, res.data.data.id, d.file)
    if (d.original) {
      try { await occurrenceFilesService.upload(props.occurrenceId, d.original, true) } catch { toast.error(t('occurrenceDocuments.read.originalFailed')) }
    }
    toast.success(t('occurrenceDocuments.added'))
    draft.value = null
    emit('saved')
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrenceDocuments.saveFailed')))
    await load()
  } finally {
    isSaving.value = false
  }
}

async function attach(doc: OccurrenceDocument, event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  if (file.size > MAX_ATTACHMENT_BYTES) {
    toast.error(t('occurrenceDocuments.fileTooLarge'))
    return
  }
  busyId.value = doc.id
  try {
    await occurrenceDocumentsService.uploadAttachment(props.occurrenceId, doc.id, file)
    toast.success(t('occurrenceDocuments.attachmentSaved'))
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrenceDocuments.saveFailed')))
  } finally {
    busyId.value = null
  }
}

// The attachment opens in an in-app preview (a blob of the authorized response); Baixar saves it.
const preview = ref<{ name: string; mime: string; url: string; blob: Blob } | null>(null)
const previewOpen = computed({ get: () => !!preview.value, set: (v: boolean) => { if (!v) closePreview() } })
function closePreview() {
  if (preview.value) URL.revokeObjectURL(preview.value.url)
  preview.value = null
}
function savePreview() {
  if (!preview.value) return
  const a = document.createElement('a')
  a.href = preview.value.url
  a.download = preview.value.name
  a.click()
}
async function openAttachment(doc: OccurrenceDocument) {
  busyId.value = doc.id
  try {
    const res = await occurrenceDocumentsService.downloadAttachment(props.occurrenceId, doc.id)
    const blob = res.data as Blob
    // the type comes from the server's validated Content-Type, not from the file name
    const mime = (res.headers?.['content-type'] as string | undefined)?.split(';')[0] || blob.type
    preview.value = { name: doc.attachment_name || 'documento', mime, blob, url: URL.createObjectURL(new Blob([blob], { type: mime })) }
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrenceDocuments.loadFailed')))
  } finally {
    busyId.value = null
  }
}

async function confirmRemove() {
  const doc = docToDelete.value
  if (!doc) return
  busyId.value = doc.id
  try {
    await occurrenceDocumentsService.delete(props.occurrenceId, doc.id)
    toast.success(t('occurrenceDocuments.deleted'))
    docToDelete.value = null
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrenceDocuments.saveFailed')))
  } finally {
    busyId.value = null
  }
}
</script>

<template>
  <Card :class="embedded ? 'border-0 bg-transparent shadow-none' : ''">
    <CardHeader v-if="!embedded" class="pb-3 flex-row items-center justify-between space-y-0">
      <CardTitle class="text-sm font-medium flex items-center gap-2">
        <FileText class="h-4 w-4 text-sky-400 light:text-sky-600" />
        {{ t('occurrenceDocuments.cardTitle') }}
      </CardTitle>
      <Button v-if="canWrite && !draft" variant="ghost" size="sm" class="h-7" @click="draft = [newDocumentDraft()]">
        <Plus class="h-3.5 w-3.5 mr-1" />{{ t('occurrenceDocuments.addFirst') }}
      </Button>
    </CardHeader>
    <CardContent :class="embedded ? 'space-y-3 p-0 text-sm' : 'space-y-3 text-sm'">
      <div v-if="isLoading" class="flex justify-center py-2"><Loader2 class="h-4 w-4 animate-spin text-muted-foreground" /></div>
      <p v-else-if="documents.length === 0 && !draft && !embedded" class="text-muted-foreground text-xs">{{ t('occurrenceDocuments.empty') }}</p>

      <div
        v-for="doc in documents"
        :key="doc.id"
        class="rounded-lg border border-white/[0.08] light:border-gray-200 p-3 space-y-2"
      >
        <div class="flex items-center justify-between gap-2">
          <div class="flex items-center gap-2 min-w-0">
            <Badge variant="outline" class="shrink-0">{{ doc.type === 'nf' ? t('occurrenceDocuments.typeNf') : t('occurrenceDocuments.typeCupom') }}</Badge>
            <span class="font-medium tabular-nums truncate">{{ doc.number || t('occurrenceDocuments.noNumber') }}</span>
            <span v-if="doc.purchase_date" class="text-xs text-muted-foreground shrink-0">{{ formatDate(doc.purchase_date) }}</span>
          </div>
          <div class="flex items-center gap-1 shrink-0">
            <Button v-if="doc.attachment_name" variant="ghost" size="sm" class="h-7 px-2 text-xs" :disabled="busyId === doc.id" @click="openAttachment(doc)">
              <FileText class="h-3.5 w-3.5 mr-1" />{{ t('occurrenceDocuments.openFile') }}
            </Button>
            <label
              v-if="canWrite"
              class="inline-flex items-center h-7 px-2 text-xs rounded-md cursor-pointer text-muted-foreground hover:text-foreground hover:bg-white/[0.06] light:hover:bg-gray-100"
            >
              <Paperclip class="h-3.5 w-3.5 mr-1" />
              {{ doc.attachment_name ? t('occurrenceDocuments.replaceFile') : t('occurrenceDocuments.attachFile') }}
              <input type="file" class="sr-only" :accept="ATTACHMENT_ACCEPT" :disabled="busyId === doc.id" @change="attach(doc, $event)" />
            </label>
            <IconButton v-if="canWrite" :label="t('occurrenceDocuments.removeDocument')" class="h-7 w-7" :disabled="busyId === doc.id" @click="docToDelete = doc">
              <Trash2 class="h-3.5 w-3.5 text-destructive" />
            </IconButton>
          </div>
        </div>
        <ul v-if="doc.items?.length" class="text-xs space-y-0.5">
          <li v-for="(item, i) in doc.items" :key="i" class="flex gap-2">
            <span v-if="item.code" class="text-muted-foreground tabular-nums">{{ item.code }}</span>
            <span class="flex-1">{{ item.description }}</span>
            <span v-if="item.quantity" class="text-muted-foreground">{{ item.quantity }}</span>
          </li>
        </ul>
      </div>

      <template v-if="draft">
        <OccurrenceDocumentsEditor v-model="draft" single :occurrence-id="occurrenceId" />
        <div class="flex justify-end gap-2">
          <Button variant="ghost" size="sm" :disabled="isSaving" @click="draft = null">{{ t('occurrenceDocuments.cancel') }}</Button>
          <Button size="sm" :disabled="isSaving" @click="saveDraft">
            <Loader2 v-if="isSaving" class="h-3.5 w-3.5 mr-1 animate-spin" />{{ t('occurrenceDocuments.saveDocument') }}
          </Button>
        </div>
      </template>
    </CardContent>
  </Card>

  <AttachmentPreviewDialog
    v-model:open="previewOpen" :name="preview?.name ?? ''" :mime="preview?.mime ?? ''" :src="preview?.url ?? null" @download="savePreview"
  />

  <DeleteConfirmDialog
    :open="!!docToDelete"
    :title="t('occurrenceDocuments.deleteConfirm')"
    :item-name="docToDelete?.number || t('occurrenceDocuments.noNumber')"
    :is-submitting="!!busyId"
    @update:open="v => { if (!v) docToDelete = null }"
    @confirm="confirmRemove"
  />
</template>
