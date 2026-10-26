<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Cloud, Download, Eye, File as FileIcon, Image as ImageIcon, Loader2, Lock, Monitor, Paperclip, Trash2 } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { DeleteConfirmDialog } from '@/components/shared'
import NextcloudDialog from '@/components/team-chat/NextcloudDialog.vue'
import { occurrenceFilesService, type OccurrenceFileItem } from '@/services/api'
import { chatErrorCode } from '@/lib/team-chat'
import { formatDateTime } from '@/lib/utils'
import { usePendingPoll } from '@/composables/usePendingPoll'

// General documents of the case, apart from the invoice/coupon records. The server decides what
// each person may do with each file (can_download / can_delete); the buttons only mirror it.
// embedded: shown inside the single Attachments card, which owns the attach menu and the restricted choice.
const props = defineProps<{ occurrenceId: string; embedded?: boolean }>()
const { t } = useI18n()

const files = ref<OccurrenceFileItem[]>([])
const canUpload = ref(false)
const maxBytes = ref(15 << 20)
const loading = ref(true)
const busy = ref(false)
const menuOpen = ref(false)
const restricted = defineModel<boolean>('restricted', { default: true })
const ncOpen = ref(false)
const toDelete = ref<OccurrenceFileItem | null>(null)
const input = ref<HTMLInputElement | null>(null)
const unwrap = <T,>(res: { data: unknown }): T => ((res.data as { data?: unknown }).data ?? res.data) as T

const ACCEPT = '.pdf,.doc,.docx,.xls,.xlsx,.xml,.csv,.txt,.jpg,.jpeg,.png,.webp'
const KNOWN = ['file_type_not_allowed', 'invalid_file', 'file_too_large', 'forbidden', 'not_found', 'rate_limited', 'nextcloud_not_connected', 'nextcloud_auth', 'nextcloud_not_found', 'nextcloud_unreachable']

function fail(err: unknown) {
  const c = chatErrorCode(err)
  toast.error(t(KNOWN.includes(c) ? `occurrenceFiles.errors.${c}` : 'occurrenceFiles.errors.generic'))
}

async function load() {
  try {
    const res = unwrap<{ files: OccurrenceFileItem[]; can_upload: boolean; max_bytes: number }>(await occurrenceFilesService.list(props.occurrenceId))
    files.value = res.files
    canUpload.value = res.can_upload
    maxBytes.value = res.max_bytes
  } catch (err) {
    fail(err)
  } finally {
    loading.value = false
  }
}
onMounted(load)
// A file still being checked for malware: the list refreshes by itself until it is released.
usePendingPoll(() => files.value.some(f => f.scan_status === 'pending' || f.scan_status === 'error'), load)

defineExpose({
  canUpload: computed(() => canUpload.value),
  pickComputer: () => input.value?.click(),
  openNextcloud: () => { ncOpen.value = true },
  reload: load,
})
watch(() => props.occurrenceId, () => { loading.value = true; void load() })

async function onPick(list: FileList | null) {
  const picked = Array.from(list ?? [])
  if (input.value) input.value.value = ''
  for (const f of picked) {
    if (f.size > maxBytes.value) {
      toast.error(t('occurrenceFiles.errors.file_too_large'))
      continue
    }
    busy.value = true
    try {
      await occurrenceFilesService.upload(props.occurrenceId, f, restricted.value)
    } catch (err) {
      fail(err)
    } finally {
      busy.value = false
    }
  }
  await load()
}

async function fromNextcloud(e: { path: string }) {
  ncOpen.value = false
  busy.value = true
  try {
    await occurrenceFilesService.fromNextcloud(props.occurrenceId, e.path, restricted.value)
    toast.success(t('occurrenceFiles.added'))
  } catch (err) {
    fail(err)
  } finally {
    busy.value = false
  }
  await load()
}

async function remove() {
  const f = toDelete.value
  if (!f) return
  try {
    await occurrenceFilesService.remove(props.occurrenceId, f.id)
    toDelete.value = null
    await load()
  } catch (err) {
    fail(err)
  }
}

const sizeLabel = (n: number) => (n >= 1048576 ? `${(n / 1048576).toFixed(1)} MB` : n >= 1024 ? `${Math.round(n / 1024)} KB` : `${n} B`)
const href = (f: OccurrenceFileItem, inline = false) => occurrenceFilesService.url(props.occurrenceId, f.id, inline)
</script>

<template>
  <Card data-testid="occurrence-files" :class="embedded ? 'border-0 bg-transparent shadow-none' : ''">
    <CardHeader v-if="!embedded" class="flex-row items-center justify-between space-y-0 pb-3">
      <CardTitle class="flex items-center gap-2 text-sm font-medium">
        <Paperclip class="h-4 w-4" aria-hidden="true" />{{ t('occurrenceFiles.title') }}
      </CardTitle>
      <div v-if="canUpload" class="relative">
        <Button size="sm" variant="outline" :disabled="busy" aria-haspopup="menu" :aria-expanded="menuOpen" data-testid="files-attach" @click="menuOpen = !menuOpen">
          <Loader2 v-if="busy" class="mr-1.5 h-4 w-4 animate-spin" /><Paperclip v-else class="mr-1.5 h-4 w-4" aria-hidden="true" />
          {{ t('occurrenceFiles.attach') }}
        </Button>
        <div v-if="menuOpen" role="menu" class="absolute right-0 z-30 mt-1 w-64 rounded-lg border bg-popover p-1 shadow-lg" @keydown.esc="menuOpen = false">
          <button type="button" role="menuitem" class="flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-sm hover:bg-muted" data-testid="files-from-computer" @click="menuOpen = false; input?.click()">
            <Monitor class="h-4 w-4 text-muted-foreground" aria-hidden="true" />{{ t('teamChat.attachMenu.computer') }}
          </button>
          <button type="button" role="menuitem" class="flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-sm hover:bg-muted" data-testid="files-from-nextcloud" @click="menuOpen = false; ncOpen = true">
            <Cloud class="h-4 w-4 text-muted-foreground" aria-hidden="true" />{{ t('teamChat.attachMenu.nextcloud') }}
          </button>
          <label class="flex items-center gap-2 border-t px-3 py-2 text-xs text-muted-foreground">
            <input v-model="restricted" type="checkbox" class="h-3.5 w-3.5" data-testid="files-restricted">
            {{ t('occurrenceFiles.restrictedHint') }}
          </label>
        </div>
        <input ref="input" type="file" multiple class="hidden" :accept="ACCEPT" data-testid="files-input" @change="onPick(($event.target as HTMLInputElement).files)">
      </div>
    </CardHeader>
    <CardContent :class="embedded ? 'p-0 text-sm' : 'text-sm'">
      <input v-if="embedded && canUpload" ref="input" type="file" multiple class="hidden" :accept="ACCEPT" data-testid="files-input" @change="onPick(($event.target as HTMLInputElement).files)">
      <div v-if="loading" class="flex justify-center py-4"><Loader2 class="h-5 w-5 animate-spin text-muted-foreground" /></div>
      <p v-else-if="files.length === 0 && !embedded" class="text-muted-foreground">{{ t('occurrenceFiles.empty') }}</p>
      <ul v-else class="divide-y">
        <li v-for="f in files" :key="f.id" class="flex items-center gap-3 py-2" :data-testid="`file-${f.id}`">
          <ImageIcon v-if="f.kind === 'image'" class="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          <FileIcon v-else class="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          <div class="min-w-0 flex-1">
            <p class="flex items-center gap-1.5 truncate font-medium">
              <span class="truncate">{{ f.name }}</span>
              <Lock v-if="f.restricted" class="h-3 w-3 shrink-0 text-muted-foreground" :title="t('occurrenceFiles.restricted')" :aria-label="t('occurrenceFiles.restricted')" />
            </p>
            <p class="truncate text-xs text-muted-foreground">{{ sizeLabel(f.size_bytes) }} · {{ f.uploaded_by_name }} · {{ formatDateTime(f.created_at) }}</p>
            <p v-if="f.scan_status && f.scan_status !== 'clean'" class="text-xs" :class="f.scan_status === 'infected' ? 'font-medium text-destructive' : 'text-amber-600'" :data-testid="`file-scan-${f.id}`" :data-scan="f.scan_status">
              {{ t(`fileScan.${f.scan_status}`) }}
            </p>
          </div>
          <a v-if="f.can_preview" :href="href(f, true)" target="_blank" rel="noopener noreferrer" class="rounded p-1.5 hover:bg-muted" :aria-label="t('occurrenceFiles.preview')" :title="t('occurrenceFiles.preview')">
            <Eye class="h-4 w-4" aria-hidden="true" />
          </a>
          <a v-if="f.can_download" :href="href(f)" class="rounded p-1.5 hover:bg-muted" :aria-label="t('occurrenceFiles.download')" :title="t('occurrenceFiles.download')" :data-testid="`file-download-${f.id}`">
            <Download class="h-4 w-4" aria-hidden="true" />
          </a>
          <span v-else-if="!f.scan_status || f.scan_status === 'clean'" class="text-xs text-muted-foreground">{{ t('occurrenceFiles.noAccess') }}</span>
          <button v-if="f.can_delete" type="button" class="rounded p-1.5 text-destructive hover:bg-muted" :aria-label="t('common.delete')" :title="t('common.delete')" :data-testid="`file-delete-${f.id}`" @click="toDelete = f">
            <Trash2 class="h-4 w-4" aria-hidden="true" />
          </button>
        </li>
      </ul>
    </CardContent>

    <NextcloudDialog v-if="ncOpen" v-model:open="ncOpen" @picked="fromNextcloud" />
    <DeleteConfirmDialog
      :open="!!toDelete" :title="t('occurrenceFiles.deleteTitle')" :item-name="toDelete?.name"
      @update:open="(v: boolean) => { if (!v) toDelete = null }" @confirm="remove"
    >
      <p class="text-sm text-muted-foreground">{{ t('occurrenceFiles.deleteWarning') }}</p>
    </DeleteConfirmDialog>
  </Card>
</template>
