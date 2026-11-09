<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { occurrencesService, type OccurrenceTransfer } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDateTime } from '@/lib/utils'
import { ArrowRightLeft, Send, Undo2 } from 'lucide-vue-next'

// Forward and Return: the main responsibility moves between departments (teams), with a reason, and the case keeps
// its protocol, files, discussion and history. Supporting and following are separate (the card above).
const props = defineProps<{ occurrenceId: string; teamName?: string; assignedName?: string; closed?: boolean }>()
const emit = defineEmits<{ changed: [] }>()
const { t } = useI18n()

const transfers = ref<OccurrenceTransfer[]>([])
const canForward = ref(false)
const canReturn = ref(false)
const returnTo = ref('')
const busy = ref(false)

async function load() {
  try {
    const res = await occurrencesService.listTransfers(props.occurrenceId)
    const d = ((res.data as any).data ?? res.data)
    transfers.value = d.transfers ?? []
    canForward.value = !!d.can_forward
    canReturn.value = !!d.can_return
    returnTo.value = d.return_to ?? ''
  } catch {
    transfers.value = []
    canForward.value = canReturn.value = false
  }
}
watch(() => [props.occurrenceId, props.teamName, props.assignedName, props.closed], load, { immediate: true })

// ---- forward
const forwardOpen = ref(false)
const targets = ref<{ id: string; name: string; unit_name?: string }[]>([])
const members = ref<{ id: string; full_name: string }[]>([])
const teamId = ref('')
const personId = ref('')
const reason = ref('')
const reasonOk = computed(() => reason.value.trim().length >= 3)

async function openForward() {
  forwardOpen.value = true
  teamId.value = personId.value = reason.value = ''
  try {
    const res = await occurrencesService.forwardTargets()
    targets.value = (((res.data as any).data ?? res.data).targets ?? []).filter((x: { name: string }) => x.name !== props.teamName)
  } catch { targets.value = [] }
}
watch(teamId, async (id) => {
  personId.value = ''
  members.value = []
  if (!id) return
  try {
    const res = await occurrencesService.teamMembers(id)
    members.value = ((res.data as any).data ?? res.data).members ?? []
  } catch { members.value = [] }
})

async function doForward() {
  if (!teamId.value || !reasonOk.value) return
  busy.value = true
  try {
    await occurrencesService.forward(props.occurrenceId, { team_id: teamId.value, assigned_user_id: personId.value || undefined, reason: reason.value.trim() })
    toast.success(t('occurrences.transfer.forwarded'))
    forwardOpen.value = false
    emit('changed')
    await load()
  } catch (e: any) {
    toast.error(e?.response?.status === 409 ? t('occurrences.transfer.conflict') : getErrorMessage(e, t('occurrences.transfer.failed')))
    await load()
  } finally { busy.value = false }
}

// ---- return
const returnOpen = ref(false)
const returnReason = ref('')
async function doReturn() {
  if (returnReason.value.trim().length < 3) return
  busy.value = true
  try {
    await occurrencesService.returnCase(props.occurrenceId, returnReason.value.trim())
    toast.success(t('occurrences.transfer.returned'))
    returnOpen.value = false
    returnReason.value = ''
    emit('changed')
    await load()
  } catch (e: any) {
    toast.error(e?.response?.status === 409 ? t('occurrences.transfer.conflict') : getErrorMessage(e, t('occurrences.transfer.failed')))
    await load()
  } finally { busy.value = false }
}

const moves = computed(() => transfers.value.filter(x => x.reason !== 'Abertura da ocorrência').slice().reverse())
</script>

<template>
  <section class="space-y-3 text-sm" data-testid="occurrence-transfer">
    <h3 class="flex items-center gap-2 text-sm font-medium">
      <ArrowRightLeft class="h-4 w-4 text-muted-foreground" aria-hidden="true" />
      {{ $t('occurrences.transfer.title') }}
    </h3>

    <dl class="grid grid-cols-1 gap-3 sm:grid-cols-2">
      <div>
        <dt class="text-xs text-muted-foreground">{{ $t('occurrences.transfer.currentTeam') }}</dt>
        <dd class="font-medium [overflow-wrap:anywhere]" data-testid="current-team">{{ teamName || $t('occurrences.transfer.noTeam') }}</dd>
      </div>
      <div>
        <dt class="text-xs text-muted-foreground">{{ $t('occurrences.transfer.currentOwner') }}</dt>
        <dd class="font-medium [overflow-wrap:anywhere]" data-testid="current-owner">{{ assignedName || $t('occurrences.transfer.unassigned') }}</dd>
      </div>
    </dl>

    <div v-if="canForward || canReturn" class="flex flex-wrap gap-2">
      <Button v-if="canForward" size="sm" :disabled="busy" data-testid="transfer-forward" @click="openForward">
        <Send class="mr-1.5 h-4 w-4" aria-hidden="true" />{{ $t('occurrences.transfer.forward') }}
      </Button>
      <Button v-if="canReturn" size="sm" variant="outline" :disabled="busy" data-testid="transfer-return" @click="returnOpen = true">
        <Undo2 class="mr-1.5 h-4 w-4" aria-hidden="true" />{{ $t('occurrences.transfer.return') }}
      </Button>
    </div>

    <ol v-if="moves.length" class="space-y-2 border-l pl-3" data-testid="transfer-history">
      <li v-for="m in moves" :key="m.id" class="text-xs">
        <p class="font-medium">
          {{ m.kind === 'return' ? $t('occurrences.transfer.movedBack') : $t('occurrences.transfer.movedOn') }}
          <span v-if="m.from_team || m.to_team"> · {{ m.from_team || $t('occurrences.transfer.requester') }} → {{ m.to_team || $t('occurrences.transfer.requester') }}</span>
          <span v-if="m.to_user"> ({{ m.to_user }})</span>
        </p>
        <p class="text-muted-foreground">{{ m.by }} · {{ formatDateTime(m.created_at) }}</p>
        <p class="[overflow-wrap:anywhere]">{{ m.reason }}</p>
      </li>
    </ol>

    <Dialog v-model:open="forwardOpen">
      <DialogContent class="max-w-md">
        <DialogHeader>
          <DialogTitle>{{ $t('occurrences.transfer.forwardTitle') }}</DialogTitle>
          <DialogDescription>{{ $t('occurrences.transfer.forwardDesc') }}</DialogDescription>
        </DialogHeader>
        <div class="space-y-3">
          <div class="space-y-1.5">
            <Label for="fwd-team">{{ $t('occurrences.transfer.destination') }}</Label>
            <select id="fwd-team" v-model="teamId" class="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm" data-testid="forward-team">
              <option value="">{{ $t('occurrences.transfer.pickTeam') }}</option>
              <option v-for="x in targets" :key="x.id" :value="x.id">{{ x.name }}<template v-if="x.unit_name"> — {{ x.unit_name }}</template></option>
            </select>
          </div>
          <div v-if="teamId" class="space-y-1.5">
            <Label for="fwd-person">{{ $t('occurrences.transfer.person') }}</Label>
            <select id="fwd-person" v-model="personId" class="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm" data-testid="forward-person">
              <option value="">{{ $t('occurrences.transfer.anyone') }}</option>
              <option v-for="p in members" :key="p.id" :value="p.id">{{ p.full_name }}</option>
            </select>
          </div>
          <div class="space-y-1.5">
            <Label for="fwd-reason">{{ $t('occurrences.transfer.reason') }} <span class="text-destructive">*</span></Label>
            <Textarea id="fwd-reason" v-model="reason" :rows="3" :maxlength="500" data-testid="forward-reason" />
          </div>
          <p class="text-xs text-muted-foreground">{{ $t('occurrences.transfer.keeps') }}</p>
        </div>
        <DialogFooter>
          <Button variant="outline" @click="forwardOpen = false">{{ $t('common.cancel') }}</Button>
          <Button :disabled="!teamId || !reasonOk || busy" data-testid="forward-confirm" @click="doForward">{{ $t('occurrences.transfer.forward') }}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <Dialog v-model:open="returnOpen">
      <DialogContent class="max-w-md">
        <DialogHeader>
          <DialogTitle>{{ $t('occurrences.transfer.returnTitle') }}</DialogTitle>
          <DialogDescription>{{ $t('occurrences.transfer.returnDesc', { to: returnTo || $t('occurrences.transfer.requester') }) }}</DialogDescription>
        </DialogHeader>
        <div class="space-y-1.5">
          <Label for="ret-reason">{{ $t('occurrences.transfer.reason') }} <span class="text-destructive">*</span></Label>
          <Textarea id="ret-reason" v-model="returnReason" :rows="3" :maxlength="500" data-testid="return-reason" />
        </div>
        <DialogFooter>
          <Button variant="outline" @click="returnOpen = false">{{ $t('common.cancel') }}</Button>
          <Button :disabled="returnReason.trim().length < 3 || busy" data-testid="return-confirm" @click="doReturn">{{ $t('occurrences.transfer.return') }}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </section>
</template>
