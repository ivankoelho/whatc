<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { AlertCircle, Check, FileText, Image as ImageIcon, Loader2, Mic, RotateCw, X } from 'lucide-vue-next'
import { formatBytes, TEAM_CHAT_UPLOAD_ERRORS } from '@/lib/team-chat'
import type { UploadItem } from '@/stores/teamChat'

// The files waiting under the composer: one card per file with its state. The
// server decides what is acceptable; the reason of a failure is explained by its code.
defineProps<{ items: UploadItem[] }>()
const emit = defineEmits<{ retry: [key: string]; cancel: [key: string] }>()
const { t } = useI18n()

const reason = (code?: string) =>
  t(code && TEAM_CHAT_UPLOAD_ERRORS.includes(code) ? `teamChat.errors.${code}` : 'teamChat.errors.generic')
</script>

<template>
  <ul v-if="items.length" class="mb-2 flex flex-wrap gap-2" data-testid="attachment-queue" :aria-label="t('teamChat.attachments.queue')">
    <li
      v-for="u in items" :key="u.key" :data-testid="`upload-${u.key}`" :data-status="u.status"
      :class="['relative flex w-full min-w-0 items-center gap-2 rounded-md border px-2 py-1.5 text-xs sm:w-56', u.status === 'failed' ? 'border-destructive/60' : '']"
    >
      <Mic v-if="u.kind === 'audio'" class="h-4 w-4 shrink-0 text-muted-foreground" />
      <ImageIcon v-else-if="u.kind === 'image'" class="h-4 w-4 shrink-0 text-muted-foreground" />
      <FileText v-else class="h-4 w-4 shrink-0 text-muted-foreground" />

      <div class="min-w-0 flex-1">
        <p class="truncate font-medium">{{ u.name }}</p>
        <p v-if="u.status === 'failed'" class="flex items-center gap-1 text-destructive" role="alert">
          <AlertCircle class="h-3 w-3 shrink-0" />{{ reason(u.error) }}
        </p>
        <p v-else-if="u.status === 'uploading'" class="text-muted-foreground" aria-live="polite">
          {{ t('teamChat.attachments.uploading', { pct: u.progress }) }}
        </p>
        <p v-else-if="u.status === 'queued'" class="text-muted-foreground">{{ t('teamChat.attachments.queued') }}</p>
        <p v-else class="flex items-center gap-1 text-muted-foreground"><Check class="h-3 w-3" />{{ formatBytes(u.size) }}</p>
        <div
          v-if="u.status === 'uploading'" class="mt-1 h-1 overflow-hidden rounded bg-muted"
          role="progressbar" :aria-valuenow="u.progress" aria-valuemin="0" aria-valuemax="100" :aria-label="u.name"
        >
          <div class="h-full bg-primary transition-all" :style="{ width: u.progress + '%' }" />
        </div>
      </div>

      <Loader2 v-if="u.status === 'uploading'" class="h-3.5 w-3.5 shrink-0 animate-spin text-muted-foreground" />
      <button
        v-if="u.status === 'failed'" type="button" class="rounded p-2 text-muted-foreground hover:bg-muted md:p-1"
        :aria-label="t('teamChat.attachments.retry')" @click="emit('retry', u.key)"
      ><RotateCw class="h-3.5 w-3.5" /></button>
      <button
        type="button" class="rounded p-2 text-muted-foreground hover:bg-muted md:p-1"
        :aria-label="t('teamChat.attachments.remove', { name: u.name })" @click="emit('cancel', u.key)"
      ><X class="h-3.5 w-3.5" /></button>
    </li>
  </ul>
</template>
