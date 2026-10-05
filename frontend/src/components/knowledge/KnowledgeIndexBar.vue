<script setup lang="ts">
// Index status and reindex actions. Rendered only for who has knowledge:write (the status
// endpoint needs it); the reindex runs synchronously on the server, so the buttons wait.
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Alert, AlertDescription } from '@/components/ui/alert'
import type { KnowledgeIndexStatus } from '@/services/api'
import { Loader2, RefreshCw } from 'lucide-vue-next'

defineProps<{ status: KnowledgeIndexStatus | null; busy: boolean }>()
const emit = defineEmits<{ reindex: [onlyStale: boolean] }>()

const { t } = useI18n()
</script>

<template>
  <div v-if="status" class="space-y-2" data-testid="knowledge-index-bar">
    <Alert v-if="status.stale_chunks > 0" data-testid="knowledge-stale-banner">
      <AlertDescription class="flex flex-wrap items-center gap-3">
        <span>{{ t('knowledge.staleBanner', { count: status.stale_chunks, version: status.index_version }) }}</span>
        <Button size="sm" :disabled="busy" @click="emit('reindex', true)">
          <Loader2 v-if="busy" class="h-4 w-4 mr-2 animate-spin" /><RefreshCw v-else class="h-4 w-4 mr-2" />
          {{ t('knowledge.reindexStale') }}
        </Button>
      </AlertDescription>
    </Alert>
    <div class="flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
      <span>{{ t('knowledge.indexSummary', { documents: status.documents, chunks: status.chunks, version: status.index_version }) }}</span>
      <Button size="sm" variant="ghost" :disabled="busy" @click="emit('reindex', false)">{{ t('knowledge.reindexAll') }}</Button>
    </div>
  </div>
</template>
