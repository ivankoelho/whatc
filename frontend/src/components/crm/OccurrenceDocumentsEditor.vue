<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Button } from '@/components/ui/button'
import { Paperclip, Plus, Trash2, X, FileText, ScanText, Loader2 } from 'lucide-vue-next'
import { ref } from 'vue'
import { occurrenceDocumentsService, type InvoiceExtraction } from '@/services/api'
import { toast } from 'vue-sonner'
import { type DocumentDraft, newDocumentDraft, emptyDocumentItem, applyProposal, MAX_ATTACHMENT_BYTES, ATTACHMENT_ACCEPT } from '@/lib/occurrence-documents'

const props = withDefaults(defineProps<{
  modelValue: DocumentDraft[]
  // Detail view edits one document at a time.
  single?: boolean
  numberRequired?: boolean
  productRequired?: boolean
  dateRequired?: boolean
  // Known only on an existing case: it is what lets the file be read (the server checks the access).
  occurrenceId?: string
}>(), { single: false })

const emit = defineEmits<{ 'update:modelValue': [value: DocumentDraft[]] }>()
const { t } = useI18n()

function update(fn: (docs: DocumentDraft[]) => void) {
  const docs = props.modelValue.map(d => ({ ...d, items: d.items.map(i => ({ ...i })) }))
  fn(docs)
  emit('update:modelValue', docs)
}

// "Attach and read": the file is attached as usual and ALSO read to propose values. Nothing is saved
// here; the person reviews the form and confirms with the normal save button.
const reading = ref(false)
const review = ref<{ index: number; res: InvoiceExtraction; filled: string[] } | null>(null)
const ACCEPT_READ = '.xml,application/pdf,image/jpeg,image/png,image/webp'

async function onReadFile(index: number, event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0] || null
  input.value = ''
  if (!file || !props.occurrenceId) return
  if (file.size > MAX_ATTACHMENT_BYTES) {
    toast.error(t('occurrenceDocuments.fileTooLarge'))
    return
  }
  reading.value = true
  try {
    const body = (await occurrenceDocumentsService.extract(props.occurrenceId, file)).data as unknown as { data?: InvoiceExtraction }
    const res = (body.data ?? body) as InvoiceExtraction
    let filled: string[] = []
    update(docs => {
      const r = applyProposal(docs[index], { number: res.number, purchase_date: res.issue_date, items: res.items })
      filled = r.filled
      docs[index] = r.draft
      // The original file is kept as the document's attachment when it is a kind the attachment accepts.
      if (file.name.toLowerCase().endsWith('.xml')) docs[index].original = res.method === 'none' ? null : file
      else docs[index].file = file
      if (res.method !== 'none') {
        docs[index].source = res.method
        docs[index].access_key = res.access_key
      }
    })
    review.value = { index, res, filled }
  } catch {
    toast.error(t('occurrenceDocuments.read.failed'))
  } finally {
    reading.value = false
  }
}

function onFile(index: number, event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0] || null
  input.value = ''
  if (file && file.size > MAX_ATTACHMENT_BYTES) {
    toast.error(t('occurrenceDocuments.fileTooLarge'))
    return
  }
  update(docs => { docs[index].file = file })
}
</script>

<template>
  <div class="space-y-3">
    <div
      v-for="(doc, index) in modelValue"
      :key="doc.key"
      class="rounded-lg border border-white/[0.08] light:border-gray-200 p-3 space-y-3"
    >
      <div class="flex items-center justify-between gap-2">
        <div class="inline-flex rounded-md border border-white/[0.08] light:border-gray-200 p-0.5" role="radiogroup" :aria-label="t('occurrenceDocuments.typeLabel')">
          <button
            v-for="opt in (['nf', 'cupom'] as const)"
            :key="opt"
            type="button"
            role="radio"
            :aria-checked="doc.type === opt"
            :class="[
              'px-3 py-1 text-xs font-medium rounded transition-colors',
              doc.type === opt
                ? 'bg-white/[0.1] text-white light:bg-gray-900 light:text-white'
                : 'text-white/50 hover:text-white/80 light:text-gray-500 light:hover:text-gray-800'
            ]"
            @click="update(docs => { docs[index].type = opt })"
          >
            {{ opt === 'nf' ? t('occurrenceDocuments.typeNf') : t('occurrenceDocuments.typeCupom') }}
          </button>
        </div>
        <Button
          v-if="!single"
          type="button"
          variant="ghost"
          size="sm"
          class="h-7 text-muted-foreground"
          @click="update(docs => { docs.splice(index, 1) })"
        >
          <Trash2 class="h-3.5 w-3.5 mr-1" />{{ t('occurrenceDocuments.removeDocument') }}
        </Button>
      </div>

      <div class="grid grid-cols-2 gap-3">
        <div class="space-y-1.5">
          <Label :for="`doc-number-${doc.key}`">
            {{ doc.type === 'nf' ? t('occurrenceDocuments.numberNf') : t('occurrenceDocuments.numberCupom') }}
            <span v-if="numberRequired" class="text-destructive">*</span>
          </Label>
          <Input
            :id="`doc-number-${doc.key}`"
            :model-value="doc.number"
            maxlength="60"
            @update:model-value="v => update(docs => { docs[index].number = String(v) })"
          />
        </div>
        <div class="space-y-1.5">
          <Label :for="`doc-date-${doc.key}`">
            {{ t('occurrences.purchaseDateLabel') }}
            <span v-if="dateRequired" class="text-destructive">*</span>
          </Label>
          <Input
            :id="`doc-date-${doc.key}`"
            type="date"
            :model-value="doc.purchase_date"
            @update:model-value="v => update(docs => { docs[index].purchase_date = String(v) })"
          />
        </div>
      </div>

      <!-- Attachment -->
      <div class="flex items-center gap-2 text-sm">
        <label
          v-if="!doc.file"
          class="inline-flex items-center gap-1.5 cursor-pointer rounded-md border border-dashed border-white/[0.15] light:border-gray-300 px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground hover:border-white/30"
        >
          <Paperclip class="h-3.5 w-3.5" />
          {{ doc.type === 'nf' ? t('occurrenceDocuments.attachNf') : t('occurrenceDocuments.attachCupom') }}
          <input type="file" class="sr-only" :accept="ATTACHMENT_ACCEPT" @change="onFile(index, $event)" />
        </label>
        <span v-else class="inline-flex items-center gap-1.5 rounded-md bg-white/[0.06] light:bg-gray-100 px-2.5 py-1 text-xs">
          <FileText class="h-3.5 w-3.5 text-sky-400 light:text-sky-600" />
          <span class="max-w-[220px] truncate">{{ doc.file.name }}</span>
          <button type="button" class="text-muted-foreground hover:text-foreground" :aria-label="t('occurrenceDocuments.removeFile')" @click="update(docs => { docs[index].file = null })">
            <X class="h-3.5 w-3.5" />
          </button>
        </span>
        <span class="text-[11px] text-muted-foreground">{{ t('occurrenceDocuments.attachHint') }}</span>
      </div>

      <!-- Read an invoice: proposes values, the person confirms -->
      <div v-if="occurrenceId && doc.type === 'nf'" class="space-y-2">
        <label class="inline-flex w-fit cursor-pointer items-center gap-1.5 rounded-md border px-3 py-1.5 text-xs hover:bg-muted" :class="reading ? 'pointer-events-none opacity-60' : ''">
          <Loader2 v-if="reading" class="h-3.5 w-3.5 animate-spin" /><ScanText v-else class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('occurrenceDocuments.read.button') }}
          <input type="file" class="sr-only" :accept="ACCEPT_READ" data-testid="read-invoice-input" @change="onReadFile(index, $event)" />
        </label>
        <div v-if="review && review.index === index" class="rounded-md border border-amber-500/40 bg-amber-500/10 p-2.5 text-xs" data-testid="extraction-review" role="status">
          <p class="font-medium">{{ review.res.method === 'none' ? t('occurrenceDocuments.read.nothing') : t('occurrenceDocuments.read.reviewTitle', { method: t('occurrenceDocuments.read.method.' + review.res.method) }) }}</p>
          <p v-if="review.filled.length" class="mt-1">{{ t('occurrenceDocuments.read.filled', { fields: review.filled.map(f => t('occurrenceDocuments.read.field.' + f)).join(', ') }) }}</p>
          <p v-if="review.res.issuer" class="mt-1 text-muted-foreground">{{ t('occurrenceDocuments.read.issuer', { name: review.res.issuer }) }}</p>
          <p v-if="review.res.access_key" class="mt-1 break-all font-mono text-[11px] text-muted-foreground">{{ review.res.access_key }}</p>
          <ul class="mt-1 list-disc pl-4">
            <li v-for="u in review.res.uncertain" :key="'u' + u">{{ t('occurrenceDocuments.read.uncertain.' + u) }}</li>
            <li v-for="w in review.res.warnings" :key="'w' + w">{{ t('occurrenceDocuments.read.warning.' + w) }}</li>
          </ul>
          <p class="mt-1 text-muted-foreground">{{ t('occurrenceDocuments.read.confirm') }}</p>
        </div>
      </div>

      <!-- Products -->
      <div class="space-y-1.5">
        <Label class="text-xs text-muted-foreground">
          {{ t('occurrenceDocuments.products') }}
          <span v-if="productRequired" class="text-destructive">*</span>
        </Label>
        <div v-for="(item, itemIndex) in doc.items" :key="itemIndex" class="flex items-center gap-2">
          <Input
            :model-value="item.code"
            :placeholder="t('occurrenceDocuments.productCode')"
            :aria-label="t('occurrenceDocuments.productCode')"
            class="w-24 shrink-0"
            @update:model-value="v => update(docs => { docs[index].items[itemIndex].code = String(v) })"
          />
          <Input
            :model-value="item.description"
            :placeholder="t('occurrences.productPlaceholder')"
            :aria-label="t('occurrenceDocuments.productDescription')"
            class="flex-1"
            @update:model-value="v => update(docs => { docs[index].items[itemIndex].description = String(v) })"
          />
          <Input
            :model-value="item.quantity"
            :placeholder="t('occurrenceDocuments.productQuantity')"
            :aria-label="t('occurrenceDocuments.productQuantity')"
            class="w-24 shrink-0"
            @update:model-value="v => update(docs => { docs[index].items[itemIndex].quantity = String(v) })"
          />
          <button
            type="button"
            class="text-muted-foreground hover:text-destructive disabled:opacity-30"
            :disabled="doc.items.length === 1"
            :aria-label="t('occurrenceDocuments.removeProduct')"
            @click="update(docs => { docs[index].items.splice(itemIndex, 1) })"
          >
            <Trash2 class="h-4 w-4" />
          </button>
        </div>
        <Button type="button" variant="ghost" size="sm" class="h-7 px-2 text-xs" @click="update(docs => { docs[index].items.push(emptyDocumentItem()) })">
          <Plus class="h-3.5 w-3.5 mr-1" />{{ t('occurrenceDocuments.addProduct') }}
        </Button>
      </div>
    </div>

    <Button v-if="!single" type="button" variant="outline" size="sm" @click="update(docs => { docs.push(newDocumentDraft()) })">
      <Plus class="h-4 w-4 mr-1" />{{ modelValue.length ? t('occurrenceDocuments.addAnother') : t('occurrenceDocuments.addFirst') }}
    </Button>
  </div>
</template>
