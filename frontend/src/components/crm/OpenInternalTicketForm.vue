<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import { CheckCircle2, Loader2, X } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { useUsersStore } from '@/stores/users'
import { useAuthStore } from '@/stores/auth'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  occurrencesService,
  departmentsService,
  occurrenceCategoriesService,
  type InternalTicketInput,
  type OccurrenceCategory,
} from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'

const emit = defineEmits<{ created: [occurrenceId: string]; cancel: [] }>()

const { t } = useI18n()
const router = useRouter()

const levels = ['low', 'medium', 'high'] as const
// Mirrors the server's ITIL matrix (internalPriority) only to preview it; the server decides.
const score = { low: 1, medium: 2, high: 3 } as const
const priorityPreview = computed(() => {
  if (!impact.value || !urgency.value) return ''
  const s = score[impact.value as keyof typeof score] + score[urgency.value as keyof typeof score]
  return s >= 6 ? 'urgent' : s === 5 ? 'high' : s === 4 ? 'normal' : 'low'
})

const title = ref('')
const description = ref('')
const ticketType = ref<'incident' | 'request'>('request')
const impact = ref('')
const urgency = ref('')
const teamId = ref('')
const departmentId = ref('')
const categoryId = ref('')
const requesterDepartmentId = ref('')
const assigneeId = ref('')
const dueDate = ref('')
const affectedItem = ref('')

const teams = ref<{ id: string; name: string; unit_name?: string }[]>([])
const departments = ref<{ id: string; name: string }[]>([])
const categories = ref<OccurrenceCategory[]>([])
const members = ref<{ id: string; full_name: string }[]>([])

// Observers follow the ticket (read plus comments). Anyone but the requester.
const usersStore = useUsersStore()
const authStore = useAuthStore()
const observerIds = ref<string[]>([])
const observerPick = ref('')
const observerOptions = computed(() =>
  usersStore.users.filter(u => u.is_active && u.id !== authStore.user?.id && !observerIds.value.includes(u.id)))
function addObserver(id: string) {
  if (id && !observerIds.value.includes(id)) observerIds.value.push(id)
  observerPick.value = ''
}
function observerName(id: string) { return usersStore.users.find(u => u.id === id)?.full_name || id }

const submitting = ref(false)
const result = ref<{ id: string; protocol: string; title: string } | null>(null)

const canSubmit = computed(() =>
  title.value.trim() !== '' && description.value.trim() !== '' && (teamId.value !== '' || departmentId.value !== ''))

onMounted(async () => {
  // Without permission to list users the observer picker simply stays empty.
  if (usersStore.users.length === 0) usersStore.fetchUsers().catch(() => {})
  const [tg, dp, ct] = await Promise.allSettled([
    occurrencesService.forwardTargets(),
    departmentsService.list(),
    occurrenceCategoriesService.list(),
  ])
  // Each picker is optional on its own; a missing permission just hides it.
  if (tg.status === 'fulfilled') teams.value = ((tg.value.data as any).data || tg.value.data).targets || []
  if (dp.status === 'fulfilled') departments.value = ((dp.value.data as any).data || dp.value.data).departments || []
  if (ct.status === 'fulfilled') categories.value = ((ct.value.data as any).data || ct.value.data).categories || []
})

watch(teamId, async id => {
  assigneeId.value = ''
  members.value = []
  if (!id) return
  try {
    const res = await occurrencesService.teamMembers(id)
    members.value = ((res.data as any).data || res.data).members || []
  } catch { /* assignee stays optional */ }
})

async function submit() {
  if (!canSubmit.value || submitting.value) return
  submitting.value = true
  const body: InternalTicketInput = {
    title: title.value.trim(),
    description: description.value.trim(),
    ticket_type: ticketType.value,
    impact: impact.value || undefined,
    urgency: urgency.value || undefined,
    team_id: teamId.value || undefined,
    department_id: departmentId.value || undefined,
    category_id: categoryId.value || undefined,
    requester_department_id: requesterDepartmentId.value || undefined,
    assigned_user_id: assigneeId.value || undefined,
    due_date: dueDate.value || undefined,
    affected_item: affectedItem.value.trim() || undefined,
    observer_user_ids: observerIds.value.length ? observerIds.value : undefined,
  }
  try {
    const res = await occurrencesService.createInternal(body)
    const occ = (res.data as any).data || res.data
    result.value = { id: occ.id, protocol: occ.protocol_number, title: occ.title }
    emit('created', occ.id)
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.internal.createFailed')))
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Card class="max-w-3xl">
    <CardContent v-if="result" class="py-10 text-center space-y-4" data-testid="internal-ticket-created">
      <CheckCircle2 class="h-12 w-12 text-emerald-500 mx-auto" />
      <div>
        <h3 class="text-lg font-semibold">{{ t('occurrences.internal.createdTitle') }}</h3>
        <p class="font-mono text-sm text-muted-foreground mt-1">{{ result.protocol }}</p>
        <p class="text-sm mt-1">{{ result.title }}</p>
      </div>
      <Button @click="router.push({ name: 'occurrence-detail', params: { id: result.id } })">
        {{ t('occurrences.internal.openTicket') }}
      </Button>
    </CardContent>

    <template v-else>
      <CardHeader>
        <CardTitle>{{ t('occurrences.internal.formTitle') }}</CardTitle>
      </CardHeader>
      <CardContent class="space-y-5">
        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-1.5">
            <Label for="it-type">{{ t('occurrences.internal.type') }}</Label>
            <Select v-model="ticketType">
              <SelectTrigger id="it-type"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="request">{{ t('occurrences.internal.typeRequest') }}</SelectItem>
                <SelectItem value="incident">{{ t('occurrences.internal.typeIncident') }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-1.5">
            <Label for="it-requester-dept">{{ t('occurrences.internal.requesterDepartment') }}</Label>
            <Select v-model="requesterDepartmentId">
              <SelectTrigger id="it-requester-dept"><SelectValue :placeholder="t('occurrences.internal.optional')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="d in departments" :key="d.id" :value="d.id">{{ d.name }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        <div class="space-y-1.5">
          <Label for="it-title">{{ t('occurrences.internal.subject') }} *</Label>
          <Input id="it-title" v-model="title" maxlength="255" :placeholder="t('occurrences.internal.subjectPlaceholder')" />
        </div>
        <div class="space-y-1.5">
          <Label for="it-description">{{ t('occurrences.internal.description') }} *</Label>
          <Textarea id="it-description" v-model="description" :rows="5" :placeholder="t('occurrences.internal.descriptionPlaceholder')" />
        </div>

        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-1.5">
            <Label for="it-team">{{ t('occurrences.internal.team') }}</Label>
            <Select v-model="teamId">
              <SelectTrigger id="it-team"><SelectValue :placeholder="t('occurrences.internal.teamPlaceholder')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="tm in teams" :key="tm.id" :value="tm.id">
                  {{ tm.name }}<span v-if="tm.unit_name" class="text-muted-foreground"> · {{ tm.unit_name }}</span>
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-1.5">
            <Label for="it-dept">{{ t('occurrences.internal.targetDepartment') }}</Label>
            <Select v-model="departmentId">
              <SelectTrigger id="it-dept"><SelectValue :placeholder="t('occurrences.internal.optional')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="d in departments" :key="d.id" :value="d.id">{{ d.name }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
        <p class="text-xs text-muted-foreground -mt-3">{{ t('occurrences.internal.destinationHint') }}</p>

        <div class="grid gap-4 sm:grid-cols-3">
          <div class="space-y-1.5">
            <Label for="it-impact">{{ t('occurrences.internal.impact') }}</Label>
            <Select v-model="impact">
              <SelectTrigger id="it-impact"><SelectValue :placeholder="t('occurrences.internal.optional')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="l in levels" :key="l" :value="l">{{ t(`occurrences.internal.level.${l}`) }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-1.5">
            <Label for="it-urgency">{{ t('occurrences.internal.urgency') }}</Label>
            <Select v-model="urgency">
              <SelectTrigger id="it-urgency"><SelectValue :placeholder="t('occurrences.internal.optional')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="l in levels" :key="l" :value="l">{{ t(`occurrences.internal.level.${l}`) }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-1.5">
            <Label>{{ t('occurrences.internal.priority') }}</Label>
            <div class="h-9 flex items-center text-sm" data-testid="internal-priority-preview">
              {{ priorityPreview ? t(`occurrences.internal.priorityLabel.${priorityPreview}`) : '—' }}
            </div>
          </div>
        </div>

        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-1.5">
            <Label for="it-category">{{ t('occurrences.internal.category') }}</Label>
            <Select v-model="categoryId">
              <SelectTrigger id="it-category"><SelectValue :placeholder="t('occurrences.internal.optional')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="c in categories.filter(c => c.is_active !== false)" :key="c.id" :value="c.id">{{ c.name }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div v-if="members.length" class="space-y-1.5">
            <Label for="it-assignee">{{ t('occurrences.internal.assignee') }}</Label>
            <Select v-model="assigneeId">
              <SelectTrigger id="it-assignee"><SelectValue :placeholder="t('occurrences.internal.queueDefault')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="m in members" :key="m.id" :value="m.id">{{ m.full_name }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-1.5">
            <Label for="it-item">{{ t('occurrences.internal.affectedItem') }}</Label>
            <Input id="it-item" v-model="affectedItem" maxlength="255" :placeholder="t('occurrences.internal.affectedItemPlaceholder')" />
          </div>
          <div class="space-y-1.5">
            <Label for="it-due">{{ t('occurrences.internal.dueDate') }}</Label>
            <Input id="it-due" v-model="dueDate" type="date" />
          </div>
        </div>

        <div v-if="usersStore.users.length" class="space-y-1.5">
          <Label for="it-observers">{{ t('occurrences.internal.observers') }}</Label>
          <Select v-model="observerPick" @update:model-value="addObserver($event as string)">
            <SelectTrigger id="it-observers"><SelectValue :placeholder="t('occurrences.internal.observersPlaceholder')" /></SelectTrigger>
            <SelectContent>
              <SelectItem v-for="u in observerOptions" :key="u.id" :value="u.id">{{ u.full_name }}</SelectItem>
            </SelectContent>
          </Select>
          <div v-if="observerIds.length" class="flex flex-wrap gap-1.5 pt-1" data-testid="internal-observers">
            <Badge v-for="id in observerIds" :key="id" variant="secondary" class="gap-1 pr-1">
              {{ observerName(id) }}
              <button type="button" class="rounded-sm hover:bg-black/10 p-0.5" :aria-label="t('occurrences.internal.removeObserver')" @click="observerIds = observerIds.filter(x => x !== id)"><X class="h-3 w-3" /></button>
            </Badge>
          </div>
          <p class="text-xs text-muted-foreground">{{ t('occurrences.internal.observersHint') }}</p>
        </div>

        <div class="flex justify-end gap-2 pt-2">
          <Button variant="outline" :disabled="submitting" @click="emit('cancel')">{{ t('common.cancel') }}</Button>
          <Button :disabled="!canSubmit || submitting" data-testid="internal-ticket-submit" @click="submit">
            <Loader2 v-if="submitting" class="h-4 w-4 mr-2 animate-spin" />
            {{ t('occurrences.internal.submit') }}
          </Button>
        </div>
      </CardContent>
    </template>
  </Card>
</template>
