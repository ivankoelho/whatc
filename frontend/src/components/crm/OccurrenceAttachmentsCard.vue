<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, Cloud, FileText, Monitor, Paperclip, Receipt } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import OccurrenceDocumentsCard from '@/components/crm/OccurrenceDocumentsCard.vue'
import OccurrenceFilesCard from '@/components/crm/OccurrenceFilesCard.vue'

// ONE place to attach anything to the case: an invoice, a coupon/order number, or a file (from this
// computer or Nextcloud). The two lists below keep their own rules; this card only gives them a single
// entry point, so nobody has to know which of two cards a document belongs to.
defineProps<{ occurrenceId: string; canWrite: boolean }>()
const { t } = useI18n()

const docs = ref<InstanceType<typeof OccurrenceDocumentsCard> | null>(null)
const files = ref<InstanceType<typeof OccurrenceFilesCard> | null>(null)
const menuOpen = ref(false)
const restricted = ref(true)

function choose(what: 'nf' | 'cupom' | 'computer' | 'nextcloud') {
  menuOpen.value = false
  if (what === 'nf' || what === 'cupom') docs.value?.startDraft(what)
  else if (what === 'computer') files.value?.pickComputer()
  else files.value?.openNextcloud()
}
</script>

<template>
  <Card data-testid="occurrence-attachments">
    <CardHeader class="flex-row items-center justify-between space-y-0 pb-3">
      <CardTitle class="flex items-center gap-2 text-sm font-medium">
        <Paperclip class="h-4 w-4" aria-hidden="true" />{{ t('occurrenceAttach.title') }}
      </CardTitle>
      <div v-if="canWrite" class="relative" @keydown.esc="menuOpen = false">
        <Button size="sm" variant="outline" aria-haspopup="menu" :aria-expanded="menuOpen" data-testid="attach-any" @click="menuOpen = !menuOpen">
          <Paperclip class="mr-1.5 h-4 w-4" aria-hidden="true" />{{ t('occurrenceAttach.attach') }}<ChevronDown class="ml-1.5 h-3.5 w-3.5" aria-hidden="true" />
        </Button>
        <div v-if="menuOpen" role="menu" class="absolute right-0 z-30 mt-1 w-72 rounded-lg border bg-popover p-1 shadow-lg">
          <button type="button" role="menuitem" class="flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-sm hover:bg-muted" data-testid="attach-nf" @click="choose('nf')">
            <FileText class="h-4 w-4 text-sky-600" aria-hidden="true" />{{ t('occurrenceAttach.nf') }}
          </button>
          <button type="button" role="menuitem" class="flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-sm hover:bg-muted" data-testid="attach-cupom" @click="choose('cupom')">
            <Receipt class="h-4 w-4 text-sky-600" aria-hidden="true" />{{ t('occurrenceAttach.cupom') }}
          </button>
          <div class="my-1 border-t" />
          <template v-if="files?.canUpload">
            <button type="button" role="menuitem" class="flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-sm hover:bg-muted" data-testid="attach-computer" @click="choose('computer')">
              <Monitor class="h-4 w-4 text-muted-foreground" aria-hidden="true" />{{ t('teamChat.attachMenu.computer') }}
            </button>
            <button type="button" role="menuitem" class="flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-sm hover:bg-muted" data-testid="attach-nextcloud" @click="choose('nextcloud')">
              <Cloud class="h-4 w-4 text-muted-foreground" aria-hidden="true" />{{ t('teamChat.attachMenu.nextcloud') }}
            </button>
            <label class="flex items-center gap-2 border-t px-3 py-2 text-xs text-muted-foreground">
              <input v-model="restricted" type="checkbox" class="h-3.5 w-3.5" data-testid="attach-restricted">
              {{ t('occurrenceFiles.restrictedHint') }}
            </label>
          </template>
        </div>
      </div>
    </CardHeader>
    <CardContent class="space-y-4 text-sm">
      <OccurrenceDocumentsCard ref="docs" embedded :occurrence-id="occurrenceId" :can-write="canWrite" @saved="files?.reload()" />
      <OccurrenceFilesCard ref="files" v-model:restricted="restricted" embedded :occurrence-id="occurrenceId" />
    </CardContent>
  </Card>
</template>
