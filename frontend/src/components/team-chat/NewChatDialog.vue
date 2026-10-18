<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2 } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useTeamChatStore } from '@/stores/teamChat'
import { getErrorMessage } from '@/lib/api-utils'

// One dialog for both flows: a private channel (name + members) or a DM (pick
// one person). The people list comes from /team-chat/users, which only carries
// id and name for people of the caller's own organization.
const props = defineProps<{ open: boolean; mode: 'channel' | 'dm' }>()
const emit = defineEmits<{
  'update:open': [value: boolean]
  created: [channelId: string]
}>()

const { t } = useI18n()
const store = useTeamChatStore()

const name = ref('')
const search = ref('')
const picked = ref<string[]>([])
const saving = ref(false)

const people = computed(() => {
  const q = search.value.trim().toLowerCase()
  return store.users.filter(u => !q || u.full_name.toLowerCase().includes(q))
})
const canSubmit = computed(() => props.mode === 'dm' ? picked.value.length === 1 : name.value.trim().length > 0)

watch(() => props.open, open => {
  if (open) {
    name.value = ''
    search.value = ''
    picked.value = []
    void store.fetchUsers()
  }
})

function toggle(id: string) {
  if (props.mode === 'dm') {
    picked.value = [id]
    return
  }
  picked.value = picked.value.includes(id) ? picked.value.filter(x => x !== id) : [...picked.value, id]
}

async function submit() {
  if (!canSubmit.value || saving.value) return
  saving.value = true
  try {
    const ch = props.mode === 'dm'
      ? await store.openDM(picked.value[0])
      : await store.createChannel(name.value.trim(), picked.value)
    emit('update:open', false)
    emit('created', ch.id)
  } catch (err) {
    toast.error(getErrorMessage(err, t('teamChat.createFailed')))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="max-w-md">
      <DialogHeader>
        <DialogTitle>{{ mode === 'dm' ? t('teamChat.newDm') : t('teamChat.newChannel') }}</DialogTitle>
        <DialogDescription>
          {{ mode === 'dm' ? t('teamChat.newDmDesc') : t('teamChat.newChannelDesc') }}
        </DialogDescription>
      </DialogHeader>

      <form class="space-y-4" @submit.prevent="submit">
        <div v-if="mode === 'channel'" class="space-y-1.5">
          <Label for="team-chat-channel-name">{{ t('teamChat.channelName') }}</Label>
          <Input id="team-chat-channel-name" v-model="name" maxlength="100" autocomplete="off" />
        </div>

        <div class="space-y-1.5">
          <Label for="team-chat-people-search">
            {{ mode === 'dm' ? t('teamChat.pickPerson') : t('teamChat.pickMembers') }}
          </Label>
          <Input id="team-chat-people-search" v-model="search" :placeholder="t('teamChat.searchPeople')" autocomplete="off" />
          <ul class="max-h-56 overflow-y-auto rounded-md border" role="listbox" :aria-multiselectable="mode === 'channel'">
            <li v-if="people.length === 0" class="px-3 py-4 text-center text-sm text-muted-foreground">
              {{ t('teamChat.noPeople') }}
            </li>
            <li v-for="u in people" :key="u.id">
              <button
                type="button"
                role="option"
                :aria-selected="picked.includes(u.id)"
                :class="[
                  'flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-muted',
                  picked.includes(u.id) ? 'bg-muted font-medium' : ''
                ]"
                @click="toggle(u.id)"
              >
                <span :class="['h-2 w-2 rounded-full', u.is_online ? 'bg-emerald-500' : 'bg-muted-foreground/40']" />
                <span class="flex-1 truncate">{{ u.full_name }}</span>
                <span v-if="picked.includes(u.id)" aria-hidden="true">✓</span>
              </button>
            </li>
          </ul>
        </div>

        <div class="flex justify-end gap-2">
          <Button type="button" variant="ghost" @click="emit('update:open', false)">{{ t('common.cancel') }}</Button>
          <Button type="submit" :disabled="!canSubmit || saving">
            <Loader2 v-if="saving" class="mr-1 h-4 w-4 animate-spin" />
            {{ mode === 'dm' ? t('teamChat.startDm') : t('common.create') }}
          </Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>
</template>
