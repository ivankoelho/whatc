<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { occurrencesService, type OccurrenceParticipant } from '@/services/api'
import { useUsersStore } from '@/stores/users'
import { getErrorMessage } from '@/lib/api-utils'
import { Users, X } from 'lucide-vue-next'

const props = defineProps<{ occurrenceId: string; canWrite: boolean; embedded?: boolean }>()

const { t } = useI18n()
const usersStore = useUsersStore()
const participants = ref<OccurrenceParticipant[]>([])
const newUserId = ref('')
const isBusy = ref(false)

// Only people who are not already following can be picked. The list of users
// comes from the users store, which stays empty for roles without users:read:
// those can still follow and leave, just not invite.
const candidates = computed(() =>
  usersStore.users.filter(u => u.is_active && !participants.value.some(p => p.user_id === u.id)),
)

async function load() {
  try {
    const res = await occurrencesService.listParticipants(props.occurrenceId)
    const body = res.data as any
    participants.value = (body.data ?? body).participants ?? []
  } catch {
    participants.value = []
  }
}

async function add() {
  if (!newUserId.value) return
  isBusy.value = true
  try {
    await occurrencesService.addParticipant(props.occurrenceId, newUserId.value)
    newUserId.value = ''
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.participantsAddFailed')))
  } finally {
    isBusy.value = false
  }
}

async function remove(userId: string) {
  isBusy.value = true
  try {
    await occurrencesService.removeParticipant(props.occurrenceId, userId)
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.participantsRemoveFailed')))
  } finally {
    isBusy.value = false
  }
}

watch(() => props.occurrenceId, load, { immediate: true })
</script>

<template>
  <Card data-testid="occurrence-participants" :class="embedded ? 'border-0 bg-transparent shadow-none' : ''">
    <CardHeader :class="embedded ? 'space-y-0 p-0 pb-2' : 'pb-3'">
      <CardTitle class="text-sm font-medium flex items-center gap-2">
        <Users class="h-4 w-4" aria-hidden="true" />
        {{ $t('occurrences.participants') }}
      </CardTitle>
    </CardHeader>
    <CardContent :class="embedded ? 'space-y-3 p-0 text-sm' : 'space-y-3 text-sm'">
      <p v-if="participants.length === 0" class="text-muted-foreground">{{ $t('occurrences.participantsNone') }}</p>
      <ul v-else class="space-y-2">
        <li v-for="p in participants" :key="p.user_id" class="flex items-center justify-between gap-2">
          <div class="flex flex-wrap items-center gap-2 min-w-0">
            <span class="truncate font-medium">{{ p.user_name }}</span>
            <Badge variant="secondary" class="text-xs">{{ $t('occurrences.cap.view') }}</Badge>
            <Badge v-for="c in p.capabilities" :key="c" variant="outline" class="text-xs">{{ $t('occurrences.cap.' + c) }}</Badge>
          </div>
          <Button v-if="canWrite" variant="ghost" size="icon" class="h-7 w-7" :disabled="isBusy" :aria-label="$t('occurrences.participantsRemove')" @click="remove(p.user_id)">
            <X class="h-4 w-4" />
          </Button>
        </li>
      </ul>
      <div v-if="canWrite && candidates.length > 0" class="flex gap-2">
        <select v-model="newUserId" class="flex h-9 flex-1 rounded-md border border-input bg-background px-3 text-sm" data-testid="participant-select">
          <option value="">{{ $t('occurrences.participantsPick') }}</option>
          <option v-for="u in candidates" :key="u.id" :value="u.id">{{ u.full_name }}</option>
        </select>
        <Button size="sm" :disabled="!newUserId || isBusy" data-testid="participant-add" @click="add">{{ $t('occurrences.participantsAdd') }}</Button>
      </div>
    </CardContent>
  </Card>
</template>
