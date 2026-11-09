<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Download, FileQuestion, Loader2 } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'

// A preview of an attachment INSIDE the application. Viewing and downloading are separate actions: this
// only shows images, PDF and plain text, from a URL the server already authorized (and only served after
// the antimalware verdict); anything else says so and offers the authorized download. The server decides
// what is inline (from the bytes, not the extension); this component never renders HTML, SVG or any
// markup: text is shown as text, images in <img>, PDF in an <iframe> pointing at the authorized URL.
const props = defineProps<{
  open: boolean
  name: string
  mime: string
  /** Authorized URL (or blob URL) of the inline version. */
  src: string | null
  loading?: boolean
  error?: boolean
}>()
const emit = defineEmits<{ 'update:open': [boolean]; download: [] }>()
const { t } = useI18n()

const kind = computed<'image' | 'pdf' | 'text' | 'none'>(() => {
  const m = (props.mime || '').toLowerCase()
  if (/^image\/(png|jpeg|gif|webp)$/.test(m)) return 'image'
  if (m === 'application/pdf') return 'pdf'
  if (m === 'text/plain') return 'text'
  return 'none'
})

// A PDF is fetched with the user's credentials (so authorization and the antimalware gate apply) and shown from
// a blob: the endpoint itself answers with a sandboxing CSP and X-Frame-Options: DENY, which keep it from ever
// being framed or interpreted as a page, while the PDF viewer still renders the inert blob.
const pdfUrl = ref<string | null>(null)
const pdfFailed = ref(false)
function dropPdf() {
  if (pdfUrl.value && pdfUrl.value.startsWith('blob:') && pdfUrl.value !== props.src) URL.revokeObjectURL(pdfUrl.value)
  pdfUrl.value = null
}
watch(() => [props.open, props.src, kind.value] as const, async ([open, src, k]) => {
  dropPdf()
  pdfFailed.value = false
  if (!open || !src || k !== 'pdf') return
  if (src.startsWith('blob:')) { pdfUrl.value = src; return }
  try {
    const res = await fetch(src, { credentials: 'include' })
    if (!res.ok) throw new Error(String(res.status))
    pdfUrl.value = URL.createObjectURL(new Blob([await res.blob()], { type: 'application/pdf' }))
  } catch {
    pdfFailed.value = true
  }
}, { immediate: true })
onBeforeUnmount(dropPdf)

// plain text is fetched and shown as TEXT (never interpreted)
const text = ref('')
const textFailed = ref(false)
watch(() => [props.open, props.src, kind.value] as const, async ([open, src, k]) => {
  text.value = ''
  textFailed.value = false
  if (!open || !src || k !== 'text') return
  try {
    const res = await fetch(src, { credentials: 'include' })
    const body = await res.text()
    text.value = body.length > 200_000 ? body.slice(0, 200_000) + '\n…' : body
  } catch {
    textFailed.value = true
  }
}, { immediate: true })
</script>

<template>
  <Dialog :open="open" @update:open="(v: boolean) => emit('update:open', v)">
    <DialogContent class="flex h-[85vh] max-w-4xl flex-col gap-3 p-4" data-testid="attachment-preview">
      <DialogHeader>
        <DialogTitle class="truncate pr-6 text-base">{{ name }}</DialogTitle>
        <DialogDescription class="sr-only">{{ t('attachmentPreview.description') }}</DialogDescription>
      </DialogHeader>

      <div class="min-h-0 flex-1 overflow-auto rounded-md border bg-muted/20">
        <div v-if="loading" class="flex h-full items-center justify-center"><Loader2 class="h-6 w-6 animate-spin text-muted-foreground" aria-hidden="true" /></div>
        <div v-else-if="error || (kind !== 'none' && !src)" class="flex h-full flex-col items-center justify-center gap-2 p-6 text-center text-sm text-muted-foreground" role="alert">
          <FileQuestion class="h-8 w-8" aria-hidden="true" />
          <p>{{ t('attachmentPreview.failed') }}</p>
        </div>
        <img v-else-if="kind === 'image' && src" :src="src" :alt="name" class="mx-auto max-h-full max-w-full object-contain" data-testid="preview-image">
        <div v-else-if="kind === 'pdf' && pdfFailed" class="flex h-full items-center justify-center p-6 text-sm text-muted-foreground" role="alert">{{ t('attachmentPreview.failed') }}</div>
        <iframe v-else-if="kind === 'pdf' && pdfUrl" :src="pdfUrl" :title="name" class="h-full w-full" data-testid="preview-pdf" />
        <div v-else-if="kind === 'pdf'" class="flex h-full items-center justify-center"><Loader2 class="h-6 w-6 animate-spin text-muted-foreground" aria-hidden="true" /></div>
        <pre v-else-if="kind === 'text'" class="whitespace-pre-wrap break-words p-4 text-sm" data-testid="preview-text">{{ textFailed ? t('attachmentPreview.failed') : text }}</pre>
        <div v-else class="flex h-full flex-col items-center justify-center gap-2 p-6 text-center text-sm text-muted-foreground" data-testid="preview-unsupported">
          <FileQuestion class="h-8 w-8" aria-hidden="true" />
          <p>{{ t('attachmentPreview.unsupported') }}</p>
        </div>
      </div>

      <div class="flex justify-end">
        <Button variant="outline" data-testid="preview-download" @click="emit('download')">
          <Download class="mr-1.5 h-4 w-4" aria-hidden="true" />{{ t('attachmentPreview.download') }}
        </Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
