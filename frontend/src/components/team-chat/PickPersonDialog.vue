<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useTeamChatStore } from '@/stores/teamChat'

// Picks ONE person of the organization (for "add member"). The list comes from
// /team-chat/users (id and name only) and already leaves out the caller; people who
// are in the channel are filtered here so nobody is offered twice.
const props = defineProps<{ open: boolean; title: string; excludeIds: string[] }>()
const emit = defineEmits<{
  'update:open': [value: boolean]
  pick: [userId: string]
}>()

const { t } = useI18n()
const store = useTeamChatStore()
const search = ref('')

watch(() => props.open, open => {
  if (open) {
    search.value = ''
    void store.fetchUsers()
  }
})

const people = computed(() => {
  const q = search.value.trim().toLowerCase()
  const skip = new Set(props.excludeIds)
  return store.users.filter(u => !skip.has(u.id) && (!q || u.full_name.toLowerCase().includes(q)))
})
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="max-w-md">
      <DialogHeader>
        <DialogTitle>{{ title }}</DialogTitle>
        <DialogDescription>{{ t('teamChat.manage.pickDesc') }}</DialogDescription>
      </DialogHeader>
      <div class="space-y-1.5">
        <Label for="team-chat-pick-search">{{ t('teamChat.searchPeople') }}</Label>
        <Input id="team-chat-pick-search" v-model="search" autocomplete="off" />
        <ul class="max-h-56 overflow-y-auto rounded-md border" role="listbox">
          <li v-if="people.length === 0" class="px-3 py-4 text-center text-sm text-muted-foreground">
            {{ t('teamChat.noPeople') }}
          </li>
          <li v-for="u in people" :key="u.id">
            <button
              type="button"
              role="option"
              :data-testid="`pick-${u.id}`"
              class="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-muted"
              @click="emit('pick', u.id)"
            >
              <span :class="['h-2 w-2 rounded-full', u.is_online ? 'bg-emerald-500' : 'bg-muted-foreground/40']" />
              <span class="flex-1 truncate">{{ u.full_name }}</span>
            </button>
          </li>
        </ul>
      </div>
      <div class="flex justify-end">
        <Button type="button" variant="ghost" @click="emit('update:open', false)">{{ t('common.cancel') }}</Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
