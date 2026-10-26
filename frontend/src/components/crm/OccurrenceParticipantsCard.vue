<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { occurrencesService, type OccurrenceParticipant } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { Users, X } from 'lucide-vue-next'

// "Acompanhamento e apoio": pick a support department, then only its ACTIVE members (the server filters by
// what the caller may pick from); the list keeps the history of who added/removed whom and when.
const props = defineProps<{ occurrenceId: string; canWrite: boolean; embedded?: boolean }>()

const { t } = useI18n()
const participants = ref<OccurrenceParticipant[]>([])
const departments = ref<{ id: string; name: string }[]>([])
const people = ref<{ id: string; full_name: string }[]>([])
const departmentId = ref('')
const selected = ref<string[]>([])
const isBusy = ref(false)

const activeList = computed(() => participants.value.filter(p => p.active))
const history = computed(() => participants.value.filter(p => !p.active))
// people already linked are not offered twice
const candidates = computed(() => people.value.filter(u => !activeList.value.some(p => p.user_id === u.id)))

const fmt = (iso?: string) => iso
  ? new Intl.DateTimeFormat('pt-BR', { day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(iso)).replace(',', '')
  : ''
const roleLabel = (r: string) => (r === 'support' ? t('occurrences.support.roleSupport') : t('occurrences.support.roleFollower'))

async function load() {
  try {
    const res = await occurrencesService.listParticipants(props.occurrenceId)
    const body = res.data as any
    participants.value = (body.data ?? body).participants ?? []
  } catch {
    participants.value = []
  }
}

async function loadDepartments() {
  if (!props.canWrite) return
  try {
    const res = await occurrencesService.supportDepartments(props.occurrenceId)
    departments.value = ((res.data as any).data ?? res.data).departments ?? []
  } catch {
    departments.value = []
  }
}

watch(departmentId, async (id) => {
  selected.value = []
  people.value = []
  if (!id) return
  try {
    const res = await occurrencesService.supportUsers(props.occurrenceId, id)
    people.value = ((res.data as any).data ?? res.data).users ?? []
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.support.loadFailed')))
  }
})

async function add() {
  if (!departmentId.value || selected.value.length === 0) return
  isBusy.value = true
  try {
    for (const id of selected.value) {
      await occurrencesService.addParticipant(props.occurrenceId, id, { role: 'support', departmentId: departmentId.value })
    }
    selected.value = []
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.participantsAddFailed')))
    await load()
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

watch(() => props.occurrenceId, () => { load(); loadDepartments() }, { immediate: true })
</script>

<template>
  <Card data-testid="occurrence-participants" :class="embedded ? 'border-0 bg-transparent shadow-none' : ''">
    <CardHeader :class="embedded ? 'space-y-0 p-0 pb-2' : 'pb-3'">
      <CardTitle class="text-sm font-medium flex items-center gap-2">
        <Users class="h-4 w-4" aria-hidden="true" />
        {{ $t('occurrences.support.title') }}
      </CardTitle>
    </CardHeader>
    <CardContent :class="embedded ? 'space-y-3 p-0 text-sm' : 'space-y-3 text-sm'">
      <p v-if="activeList.length === 0" class="text-muted-foreground">{{ $t('occurrences.support.none') }}</p>
      <ul v-else class="space-y-2">
        <li v-for="p in activeList" :key="p.user_id" class="flex items-start justify-between gap-2" :data-testid="`participant-${p.user_id}`">
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <span class="truncate font-medium">{{ p.user_name }}</span>
              <Badge :variant="p.role === 'support' ? 'default' : 'secondary'" class="text-xs">{{ roleLabel(p.role) }}</Badge>
              <Badge v-for="c in p.capabilities" :key="c" variant="outline" class="text-xs">{{ $t('occurrences.cap.' + c) }}</Badge>
            </div>
            <p class="text-xs text-muted-foreground">
              <span v-if="p.department_name">{{ p.department_name }} · </span>{{ $t('occurrences.support.addedBy', { name: p.added_by || '—' }) }} · {{ fmt(p.added_at) }}
            </p>
          </div>
          <Button v-if="canWrite" variant="ghost" size="icon" class="h-7 w-7 shrink-0" :disabled="isBusy" :aria-label="$t('occurrences.participantsRemove')" @click="remove(p.user_id)">
            <X class="h-4 w-4" />
          </Button>
        </li>
      </ul>

      <div v-if="canWrite && departments.length > 0" class="space-y-2 rounded-md border p-2">
        <label class="block text-xs font-medium" for="support-department">{{ $t('occurrences.support.department') }}</label>
        <select id="support-department" v-model="departmentId" class="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm" data-testid="support-department">
          <option value="">{{ $t('occurrences.support.pickDepartment') }}</option>
          <option v-for="d in departments" :key="d.id" :value="d.id">{{ d.name }}</option>
        </select>
        <template v-if="departmentId">
          <p v-if="candidates.length === 0" class="text-xs text-muted-foreground">{{ $t('occurrences.support.noPeople') }}</p>
          <fieldset v-else>
            <legend class="mb-1 text-xs font-medium">{{ $t('occurrences.support.people') }}</legend>
            <label v-for="u in candidates" :key="u.id" class="flex min-h-9 items-center gap-2">
              <input v-model="selected" type="checkbox" :value="u.id" :data-testid="`support-user-${u.id}`">
              <span>{{ u.full_name }}</span>
            </label>
          </fieldset>
          <Button size="sm" :disabled="selected.length === 0 || isBusy" data-testid="participant-add" @click="add">{{ $t('occurrences.support.add') }}</Button>
        </template>
        <p class="text-xs text-muted-foreground">{{ $t('occurrences.support.note') }}</p>
      </div>

      <details v-if="history.length > 0" class="text-xs" data-testid="participant-history">
        <summary class="cursor-pointer font-medium">{{ $t('occurrences.support.history') }} ({{ history.length }})</summary>
        <ul class="mt-1 space-y-1 text-muted-foreground">
          <li v-for="p in history" :key="p.user_id">
            {{ p.user_name }} · {{ roleLabel(p.role) }}<span v-if="p.department_name"> · {{ p.department_name }}</span>
            · {{ $t('occurrences.support.removedBy', { name: p.removed_by || '—' }) }} · {{ fmt(p.removed_at) }}
          </li>
        </ul>
      </details>
    </CardContent>
  </Card>
</template>
