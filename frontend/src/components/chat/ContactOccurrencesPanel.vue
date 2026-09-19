<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { useOccurrencesStore } from '@/stores/occurrences'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from '@/components/ui/dialog'
import { getErrorMessage } from '@/lib/api-utils'
import { Plus } from 'lucide-vue-next'
import OpenProtocolForm from '@/components/crm/OpenProtocolForm.vue'

const props = defineProps<{
  contactId: string
  contactPhone?: string
  contactName?: string
  sourceTransferId?: string
}>()

const { t } = useI18n()
const store = useOccurrencesStore()
const isDialogOpen = ref(false)

async function load() {
  if (!props.contactId) return
  // Stages drive the badge colour and rarely change, so fetch them once per
  // session rather than on every contact switch.
  if (store.stages.length === 0) await store.fetchStages()
  try {
    await store.fetchContactOccurrences(props.contactId)
  } catch (e) {
    toast.error(getErrorMessage(e, t('chat.occurrenceCreateFailed')))
  }
}

// Keep the dialog open: the form now continues into the review-and-send step.
function onCreated() {
  load()
}

onMounted(load)
watch(() => props.contactId, load)
</script>

<template>
  <div id="occurrences-panel" class="w-80 border-l border-white/[0.08] light:border-gray-200 bg-[#111113] light:bg-white flex flex-col h-full min-h-0">
    <div class="flex items-center justify-between px-4 py-3 border-b border-white/[0.08] light:border-gray-200 shrink-0">
      <h3 class="text-sm font-medium text-white light:text-gray-900">{{ $t('chat.occurrences') }}</h3>
      <Button variant="ghost" size="sm" @click="isDialogOpen = true">
        <Plus class="h-4 w-4 mr-1" />
        {{ $t('chat.newOccurrence') }}
      </Button>
    </div>

    <ScrollArea orientation="vertical" class="flex-1 min-h-0">
      <div class="p-4 space-y-2">
        <RouterLink
          v-for="occ in store.contactOccurrences"
          :key="occ.id"
          :to="`/crm/occurrences/${occ.id}`"
          class="block p-3 rounded-md border border-white/[0.08] light:border-gray-200 hover:bg-white/[0.04] light:hover:bg-gray-50 transition-colors"
        >
          <div class="flex items-center justify-between gap-2">
            <span class="font-mono text-xs text-white/50 light:text-muted-foreground">{{ occ.protocol_number }}</span>
            <Badge
              variant="outline"
              class="shrink-0 text-xs"
              :style="{ borderColor: store.stageColor(occ.stage_id), color: store.stageColor(occ.stage_id) }"
            >{{ occ.stage_name }}</Badge>
          </div>
          <p class="text-sm mt-1 truncate min-w-0 text-white light:text-gray-900">{{ occ.title }}</p>
        </RouterLink>

        <p
          v-if="store.contactOccurrences.length === 0"
          class="text-sm text-white/40 light:text-muted-foreground text-center py-6"
        >
          {{ $t('chat.noOccurrences') }}
        </p>
      </div>
    </ScrollArea>

    <Dialog v-model:open="isDialogOpen">
      <DialogContent class="sm:max-w-2xl max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{{ $t('occurrences.openProtocolTitle') }}</DialogTitle>
          <DialogDescription>{{ $t('occurrences.openProtocolDesc') }}</DialogDescription>
        </DialogHeader>
        <OpenProtocolForm
          v-if="isDialogOpen"
          class="max-w-none border-0 shadow-none"
          :contact-id="contactId"
          :contact-phone="contactPhone"
          :contact-name="contactName"
          :source-transfer-id="sourceTransferId"
          closable
          @created="onCreated"
          @cancel="isDialogOpen = false"
        />
      </DialogContent>
    </Dialog>
  </div>
</template>
