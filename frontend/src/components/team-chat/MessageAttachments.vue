<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ref } from 'vue'
import { Download, Eye, FileText, ShieldAlert, ShieldCheck } from 'lucide-vue-next'
import AttachmentPreviewDialog from '@/components/shared/AttachmentPreviewDialog.vue'
import { formatBytes, formatDuration, teamChatAttachmentUrl } from '@/lib/team-chat'
import type { TeamChatAttachment } from '@/services/api'

// The files of one message. Names are the server's sanitized label and are shown as
// text only. Images and audio play inline; everything else is a download card.
defineProps<{ attachments: TeamChatAttachment[] }>()
const { t } = useI18n()

// PDF and plain text can be previewed in-app (the server serves them inline only on request, only when
// cleared by the antimalware scan, and the type is the one detected from the bytes at upload).
const location = globalThis.location
const previewable = (a: TeamChatAttachment) => a.mime === 'application/pdf' || a.mime === 'text/plain'
const previewing = ref<TeamChatAttachment | null>(null)
const previewOpen = computed({ get: () => !!previewing.value, set: (v: boolean) => { if (!v) previewing.value = null } })
const inlineUrl = (a: TeamChatAttachment) => `${teamChatAttachmentUrl(a.id)}?inline=1`

// One voice note at a time: starting another pauses the rest.
function onPlay(e: Event) {
  document.querySelectorAll('audio').forEach(a => { if (a !== e.target) a.pause() })
}
</script>

<template>
  <div class="mt-1 flex flex-col gap-1.5" data-testid="attachments-list">
    <template v-for="a in attachments" :key="a.id">
      <!-- Not cleared by the antimalware scan: nothing is loaded or offered, only the state. -->
      <div
        v-if="a.scan_status && a.scan_status !== 'clean'" :data-testid="`attachment-${a.id}`" :data-scan="a.scan_status"
        class="flex w-full max-w-xs items-center gap-2 rounded-md border border-dashed px-2 py-1.5 text-xs"
        :class="a.scan_status === 'infected' ? 'border-destructive/50 text-destructive' : 'text-muted-foreground'"
      >
        <ShieldAlert v-if="a.scan_status === 'infected'" class="h-4 w-4 shrink-0" aria-hidden="true" />
        <ShieldCheck v-else class="h-4 w-4 shrink-0" aria-hidden="true" />
        <span class="min-w-0 flex-1">
          <span class="block truncate font-medium">{{ a.name }}</span>
          <span>{{ t(`fileScan.${a.scan_status}`) }}</span>
        </span>
      </div>

      <a
        v-else-if="a.kind === 'image'" :href="teamChatAttachmentUrl(a.id)" target="_blank" rel="noopener noreferrer"
        class="block w-fit" :data-testid="`attachment-${a.id}`" :aria-label="t('teamChat.attachments.openImage', { name: a.name })"
      >
        <img
          :src="teamChatAttachmentUrl(a.id)" :alt="a.name" loading="lazy"
          :width="a.width" :height="a.height"
          class="max-h-64 max-w-full rounded-md border object-contain sm:max-w-sm"
        >
      </a>

      <div v-else-if="a.kind === 'audio'" :data-testid="`attachment-${a.id}`" class="flex flex-col gap-0.5">
        <audio
          controls preload="none" :src="teamChatAttachmentUrl(a.id)" class="h-10 w-72 max-w-full"
          :aria-label="t('teamChat.attachments.voice')" @play="onPlay"
        />
        <span v-if="a.duration_ms" class="text-[11px] text-muted-foreground">{{ formatDuration(a.duration_ms) }}</span>
      </div>

      <div v-else class="flex w-full max-w-xs items-center gap-1" :data-testid="`attachment-${a.id}`">
        <a :href="teamChatAttachmentUrl(a.id)" download class="flex min-w-0 flex-1 items-center gap-2 rounded-md border px-2 py-1.5 text-xs hover:bg-muted">
          <FileText class="h-4 w-4 shrink-0 text-muted-foreground" />
          <span class="min-w-0 flex-1">
            <span class="block truncate font-medium">{{ a.name }}</span>
            <span class="text-muted-foreground">{{ formatBytes(a.size) }}</span>
          </span>
          <Download class="h-3.5 w-3.5 shrink-0 text-muted-foreground" :aria-label="t('teamChat.attachments.download')" />
        </a>
        <button v-if="previewable(a)" type="button" class="rounded-md border p-2 hover:bg-muted" :aria-label="t('attachmentPreview.view')" :title="t('attachmentPreview.view')" :data-testid="`attachment-preview-${a.id}`" @click="previewing = a">
          <Eye class="h-3.5 w-3.5" aria-hidden="true" />
        </button>
      </div>
    </template>
    <AttachmentPreviewDialog
      v-model:open="previewOpen" :name="previewing?.name ?? ''" :mime="previewing?.mime ?? ''" :src="previewing ? inlineUrl(previewing) : null"
      @download="previewing && (location.href = teamChatAttachmentUrl(previewing.id))"
    />
  </div>
</template>
