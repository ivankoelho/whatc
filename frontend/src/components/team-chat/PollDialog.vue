<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2, Plus, X } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useTeamChatStore } from '@/stores/teamChat'
import { chatErrorCode } from '@/lib/team-chat'

// Creates a poll: a question, 2 to 10 options, single or multiple choice.
const props = defineProps<{ open: boolean; channelId: string }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
const { t } = useI18n()
const store = useTeamChatStore()

const MAX_OPTIONS = 10
const question = ref('')
const options = ref<string[]>(['', ''])
const multiple = ref(false)
const saving = ref(false)

watch(() => props.open, open => {
  if (open) {
    question.value = ''
    options.value = ['', '']
    multiple.value = false
  }
})

const filled = computed(() => options.value.map(o => o.trim()).filter(Boolean))
const distinct = computed(() => new Set(filled.value.map(o => o.toLowerCase())).size === filled.value.length)
const canSubmit = computed(() => question.value.trim().length > 0 && filled.value.length >= 2 && distinct.value && !saving.value)

async function submit() {
  if (!canSubmit.value) return
  saving.value = true
  try {
    await store.createPoll(props.channelId, { question: question.value.trim(), options: filled.value, multiple: multiple.value })
    emit('update:open', false)
  } catch (err) {
    const c = chatErrorCode(err)
    toast.error(t(['archived', 'forbidden', 'not_found', 'bad_poll'].includes(c) ? `teamChat.errors.${c}` : 'teamChat.errors.generic'))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="max-w-md">
      <DialogHeader>
        <DialogTitle>{{ t('teamChat.poll.new') }}</DialogTitle>
        <DialogDescription>{{ t('teamChat.poll.hint') }}</DialogDescription>
      </DialogHeader>
      <form class="space-y-4" @submit.prevent="submit">
        <div class="space-y-1.5">
          <Label for="poll-question">{{ t('teamChat.poll.question') }}</Label>
          <Input id="poll-question" v-model="question" maxlength="300" :placeholder="t('teamChat.poll.questionPlaceholder')" data-testid="poll-question" />
        </div>
        <div class="space-y-2">
          <Label>{{ t('teamChat.poll.options') }}</Label>
          <div v-for="(_, i) in options" :key="i" class="flex items-center gap-2">
            <Input
              v-model="options[i]" maxlength="100" :aria-label="t('teamChat.poll.optionN', { n: i + 1 })"
              :placeholder="t('teamChat.poll.optionN', { n: i + 1 })" :data-testid="`poll-option-input-${i}`"
            />
            <Button v-if="options.length > 2" type="button" variant="ghost" size="icon" class="h-9 w-9 shrink-0" :aria-label="t('common.remove')" @click="options.splice(i, 1)">
              <X class="h-4 w-4" />
            </Button>
          </div>
          <Button v-if="options.length < MAX_OPTIONS" type="button" variant="ghost" size="sm" data-testid="poll-add-option" @click="options.push('')">
            <Plus class="mr-1 h-4 w-4" />{{ t('teamChat.poll.addOption') }}
          </Button>
          <p v-if="!distinct" class="text-xs text-destructive">{{ t('teamChat.poll.duplicate') }}</p>
        </div>
        <label class="flex items-center gap-2 text-sm">
          <input v-model="multiple" type="checkbox" class="h-4 w-4" data-testid="poll-multiple">
          {{ t('teamChat.poll.allowMultiple') }}
        </label>
        <div class="flex justify-end gap-2">
          <Button type="button" variant="ghost" @click="emit('update:open', false)">{{ t('common.cancel') }}</Button>
          <Button type="submit" :disabled="!canSubmit" data-testid="poll-submit">
            <Loader2 v-if="saving" class="mr-2 h-4 w-4 animate-spin" />{{ t('teamChat.poll.create') }}
          </Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>
</template>
