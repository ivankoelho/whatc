<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2 } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { getErrorMessage } from '@/lib/api-utils'
import { useOccurrencesStore } from '@/stores/occurrences'

// A person settles a send whose outcome the system could not establish, after checking the conversation with
// the customer. It sends NOTHING. The server decides who may (occurrences:resolve_send and the scope of the
// case); this only collects the decision, the confirmation that the conversation was checked and the reason.
const props = defineProps<{ open: boolean; occurrenceId: string; sendId: string }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; resolved: [state: string] }>()
const { t } = useI18n()
const store = useOccurrencesStore()

const NOTE_MIN = 10
const NOTE_MAX = 1000
const outcome = ref<'delivered' | 'not_delivered' | ''>('')
const checked = ref(false)
const note = ref('')
const working = ref(false)

watch(() => props.open, open => {
  if (!open) return
  outcome.value = ''
  checked.value = false
  note.value = ''
})

const noteLen = computed(() => note.value.trim().length)
const canSubmit = computed(() => !!outcome.value && checked.value && noteLen.value >= NOTE_MIN && noteLen.value <= NOTE_MAX && !working.value)

async function submit() {
  if (!canSubmit.value || !outcome.value) return
  working.value = true
  try {
    const d = await store.resolveProtocolSend(props.occurrenceId, props.sendId, {
      outcome: outcome.value, conversation_checked: checked.value, note: note.value.trim(),
    })
    toast.success(t('occurrences.resolveSend.done'))
    emit('resolved', d.state)
    emit('update:open', false)
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.resolveSend.failed')))
  } finally {
    working.value = false
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="max-w-md" data-testid="resolve-send-dialog">
      <DialogHeader>
        <DialogTitle>{{ t('occurrences.resolveSend.title') }}</DialogTitle>
        <DialogDescription>{{ t('occurrences.resolveSend.desc') }}</DialogDescription>
      </DialogHeader>
      <div class="space-y-4">
        <fieldset class="space-y-2">
          <legend class="text-sm font-medium">{{ t('occurrences.resolveSend.outcomeLabel') }}</legend>
          <label class="flex items-start gap-2 text-sm">
            <input v-model="outcome" type="radio" name="resolve-outcome" value="delivered" data-testid="resolve-outcome-delivered" class="mt-1">
            <span>{{ t('occurrences.resolveSend.delivered') }}</span>
          </label>
          <label class="flex items-start gap-2 text-sm">
            <input v-model="outcome" type="radio" name="resolve-outcome" value="not_delivered" data-testid="resolve-outcome-not-delivered" class="mt-1">
            <span>{{ t('occurrences.resolveSend.notDelivered') }}</span>
          </label>
          <p v-if="outcome === 'not_delivered'" class="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs" role="alert" data-testid="resolve-not-delivered-warning">
            {{ t('occurrences.resolveSend.notDeliveredWarning') }}
          </p>
        </fieldset>
        <div class="space-y-1.5">
          <Label for="resolve-note">{{ t('occurrences.resolveSend.note') }} <span class="text-destructive">*</span></Label>
          <Textarea id="resolve-note" v-model="note" :rows="3" :maxlength="NOTE_MAX" data-testid="resolve-note" />
          <p class="text-xs text-muted-foreground">{{ t('occurrences.resolveSend.noteHint', { min: NOTE_MIN }) }}</p>
        </div>
        <p class="text-xs text-muted-foreground">{{ t('occurrences.resolveSend.final') }}</p>
        <!-- The confirmation comes last, right before the buttons: it confirms what was chosen and written above. -->
        <label class="flex items-start gap-2 text-sm">
          <input v-model="checked" type="checkbox" data-testid="resolve-checked" class="mt-1">
          <span>{{ t('occurrences.resolveSend.checked') }}</span>
        </label>
      </div>
      <DialogFooter>
        <Button variant="outline" :disabled="working" @click="emit('update:open', false)">{{ t('common.cancel') }}</Button>
        <Button :disabled="!canSubmit" data-testid="resolve-confirm" @click="submit">
          <Loader2 v-if="working" class="mr-2 h-4 w-4 animate-spin" />
          {{ t('occurrences.resolveSend.confirm') }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
