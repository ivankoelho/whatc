<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Download, FileText } from 'lucide-vue-next'
import { formatBytes, formatDuration, teamChatAttachmentUrl } from '@/lib/team-chat'
import type { TeamChatAttachment } from '@/services/api'

// The files of one message. Names are the server's sanitized label and are shown as
// text only. Images and audio play inline; everything else is a download card.
defineProps<{ attachments: TeamChatAttachment[] }>()
const { t } = useI18n()

// One voice note at a time: starting another pauses the rest.
function onPlay(e: Event) {
  document.querySelectorAll('audio').forEach(a => { if (a !== e.target) a.pause() })
}
</script>

<template>
  <div class="mt-1 flex flex-col gap-1.5" data-testid="attachments-list">
    <template v-for="a in attachments" :key="a.id">
      <a
        v-if="a.kind === 'image'" :href="teamChatAttachmentUrl(a.id)" target="_blank" rel="noopener noreferrer"
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

      <a
        v-else :href="teamChatAttachmentUrl(a.id)" download :data-testid="`attachment-${a.id}`"
        class="flex w-full max-w-xs items-center gap-2 rounded-md border px-2 py-1.5 text-xs hover:bg-muted"
      >
        <FileText class="h-4 w-4 shrink-0 text-muted-foreground" />
        <span class="min-w-0 flex-1">
          <span class="block truncate font-medium">{{ a.name }}</span>
          <span class="text-muted-foreground">{{ formatBytes(a.size) }}</span>
        </span>
        <Download class="h-3.5 w-3.5 shrink-0 text-muted-foreground" :aria-label="t('teamChat.attachments.download')" />
      </a>
    </template>
  </div>
</template>
