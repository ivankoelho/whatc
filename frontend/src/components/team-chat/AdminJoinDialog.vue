<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2, ShieldAlert } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useTeamChatStore } from '@/stores/teamChat'
import { chatErrorCode } from '@/lib/team-chat'

// Entering a private channel without being a member (team_chat:join_any). The reason
// is required and is shown, as text, to everybody in the channel and kept in the audit
// log: nobody enters in silence. The server enforces all of it; this only collects it.
const props = defineProps<{ open: boolean; channelId: string; channelName: string }>()
const emit = defineEmits<{
  'update:open': [value: boolean]
  joined: []
}>()

const MIN = 5
const MAX = 500

const { t } = useI18n()
const store = useTeamChatStore()
const reason = ref('')
const saving = ref(false)

watch(() => props.open, open => { if (open) reason.value = '' })

const length = computed(() => Array.from(reason.value.trim()).length)
const valid = computed(() => length.value >= MIN && length.value <= MAX)

async function submit() {
  if (!valid.value || saving.value) return
  saving.value = true
  try {
    await store.joinChannel(props.channelId, reason.value.trim())
    toast.success(t('teamChat.admin.joined'))
    emit('update:open', false)
    emit('joined')
  } catch (err) {
    const code = chatErrorCode(err)
    toast.error(code === 'already_member' || code === 'archived' ? t(`teamChat.errors.${code}`) : t('teamChat.errors.generic'))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="max-w-md">
      <DialogHeader>
        <DialogTitle class="flex items-center gap-2">
          <ShieldAlert class="h-5 w-5 text-amber-500" />
          {{ t('teamChat.admin.joinTitle', { name: channelName }) }}
        </DialogTitle>
        <DialogDescription>{{ t('teamChat.admin.joinDesc') }}</DialogDescription>
      </DialogHeader>
      <form class="space-y-3" @submit.prevent="submit">
        <div class="space-y-1.5">
          <Label for="team-chat-join-reason">{{ t('teamChat.admin.reason') }}</Label>
          <Textarea id="team-chat-join-reason" v-model="reason" :rows="3" :maxlength="MAX + 50" />
          <p :class="['text-xs', valid || length === 0 ? 'text-muted-foreground' : 'text-destructive']">
            {{ t('teamChat.admin.reasonHint', { min: MIN, max: MAX, n: length }) }}
          </p>
        </div>
        <div class="flex justify-end gap-2">
          <Button type="button" variant="ghost" @click="emit('update:open', false)">{{ t('common.cancel') }}</Button>
          <Button type="submit" :disabled="!valid || saving">
            <Loader2 v-if="saving" class="mr-1 h-4 w-4 animate-spin" />
            {{ t('teamChat.admin.joinAction') }}
          </Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>
</template>
