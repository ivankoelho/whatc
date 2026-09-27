<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { useOccurrencesStore } from '@/stores/occurrences'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
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
  <!-- Rendered as a section inside ContactInfoPanel's own scrollable column
       (spec item 4) — no own width/border/scroll chrome, styled with the
       same semantic tokens (bg-muted/50, border-t) ContactInfoPanel's other
       sections use, instead of the standalone panel's old hardcoded
       white/[0.08] + light: classes. -->
  <div id="occurrences-panel" class="border-t pt-4">
    <div class="flex items-center justify-between py-2">
      <h5 class="text-sm font-medium">{{ $t('chat.occurrences') }}</h5>
      <Button variant="ghost" size="sm" class="h-7 px-2" @click="isDialogOpen = true">
        <Plus class="h-3.5 w-3.5 mr-1" />
        {{ $t('chat.newOccurrence') }}
      </Button>
    </div>

    <div class="space-y-2">
      <RouterLink
        v-for="occ in store.contactOccurrences"
        :key="occ.id"
        :to="`/crm/occurrences/${occ.id}`"
        class="block p-3 rounded-md border bg-muted/50 hover:bg-muted transition-colors"
      >
        <div class="flex items-center justify-between gap-2">
          <span class="font-mono text-xs text-muted-foreground">{{ occ.protocol_number }}</span>
          <Badge
            variant="outline"
            class="shrink-0 text-xs"
            :style="{ borderColor: store.stageColor(occ.stage_id), color: store.stageColor(occ.stage_id) }"
          >{{ occ.stage_name }}</Badge>
        </div>
        <p class="text-sm mt-1 truncate min-w-0 font-semibold">{{ occ.title }}</p>
      </RouterLink>

      <p
        v-if="store.contactOccurrences.length === 0"
        class="text-sm text-muted-foreground text-center py-6"
      >
        {{ $t('chat.noOccurrences') }}
      </p>
    </div>

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
