<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { contactsService } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { Loader2, Search, UserRound } from 'lucide-vue-next'

// Step 1 of "Nova oportunidade" from the CRM screen: pick the customer. The list comes from /contacts, which
// the backend limits to the customers whose conversations this person may see, so a seller can only open an
// opportunity for a customer that is theirs to serve. Step 2 is the same dialog the chat panel uses.
const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; select: [contactId: string] }>()

const { t } = useI18n()
const search = ref('')
const loading = ref(false)
const failed = ref('')
const contacts = ref<{ id: string; profile_name?: string; phone_number?: string }[]>([])
let timer: ReturnType<typeof setTimeout> | undefined

async function load() {
  loading.value = true
  failed.value = ''
  try {
    const { data } = await contactsService.list({ search: search.value || undefined, limit: 10 })
    contacts.value = data.data?.contacts ?? []
  } catch (e) {
    contacts.value = []
    failed.value = getErrorMessage(e, t('sales.pickContactFailed'))
  } finally {
    loading.value = false
  }
}

watch(() => props.open, (isOpen) => {
  if (isOpen) {
    search.value = ''
    load()
  }
})
watch(search, () => {
  clearTimeout(timer)
  timer = setTimeout(load, 250)
})

function choose(id: string) {
  emit('select', id)
  emit('update:open', false)
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="max-w-md">
      <DialogHeader>
        <DialogTitle>{{ $t('sales.pickContactTitle') }}</DialogTitle>
        <DialogDescription>{{ $t('sales.pickContactHelp') }}</DialogDescription>
      </DialogHeader>
      <div class="relative">
        <Search class="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" aria-hidden="true" />
        <Input v-model="search" class="pl-8" :placeholder="$t('sales.pickContactSearch')" :aria-label="$t('sales.pickContactSearch')" />
      </div>
      <div class="max-h-72 space-y-1 overflow-y-auto" role="list" :aria-busy="loading">
        <div v-if="loading" class="flex items-center justify-center py-6 text-muted-foreground" role="status">
          <Loader2 class="mr-2 h-4 w-4 animate-spin" aria-hidden="true" />{{ $t('common.loading') }}
        </div>
        <p v-else-if="failed" class="py-6 text-center text-sm text-destructive" role="alert">{{ failed }}</p>
        <p v-else-if="!contacts.length" class="py-6 text-center text-sm text-muted-foreground">{{ $t('sales.pickContactEmpty') }}</p>
        <Button
          v-for="c in contacts"
          v-else
          :key="c.id"
          variant="ghost"
          class="h-auto w-full justify-start gap-2 px-2 py-2 text-left"
          role="listitem"
          @click="choose(c.id)"
        >
          <UserRound class="h-4 w-4 shrink-0" aria-hidden="true" />
          <span class="min-w-0">
            <span class="block truncate text-sm">{{ c.profile_name || c.phone_number }}</span>
            <span v-if="c.profile_name" class="block truncate text-xs text-muted-foreground">{{ c.phone_number }}</span>
          </span>
        </Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
