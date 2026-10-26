<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Archive, Gavel, Loader2, Lock, Play } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { retentionService, type RetentionCategoryReport, type RetentionHold, type RetentionPolicyView } from '@/services/api'
import { formatDateTime } from '@/lib/utils'

// File retention, for administrators. What this screen can do: choose a term per category, SIMULATE what
// would become eligible (read-only), and place or revoke legal holds. What it cannot do: delete anything.
// Automatic removal does not exist in this version, and the page says so.
const { t } = useI18n()
const unwrap = <T,>(res: { data: unknown }): T => ((res.data as { data?: unknown }).data ?? res.data) as T

const loading = ref(true)
const policies = ref<RetentionPolicyView[]>([])
const notCovered = ref<string[]>([])
const executionEnabled = ref(false)
// what the person is editing, per category (saved policy until touched)
const draft = reactive<Record<string, { enabled: boolean; months: number | undefined }>>({})
const saving = ref<string | null>(null)

const simulating = ref(false)
const simulation = ref<RetentionCategoryReport[] | null>(null)
const simulatedAt = ref<string>('')
const openSamples = ref<string | null>(null)

const holds = ref<RetentionHold[]>([])
const history = ref<{ at: string; kind: string; actor?: string; summary: string }[]>([])

const holdDialog = ref(false)
const holdProtocol = ref('')
const holdReason = ref('')
const holdBusy = ref(false)
const revokeTarget = ref<RetentionHold | null>(null)
const revokeReason = ref('')

const MIN_REASON = 10
const reasonOk = (s: string) => s.trim().length >= MIN_REASON

async function load() {
  loading.value = true
  try {
    const res = unwrap<{ policies: RetentionPolicyView[]; not_covered: string[]; engine: { execution_enabled: boolean } }>(await retentionService.get())
    policies.value = res.policies
    notCovered.value = res.not_covered
    executionEnabled.value = res.engine.execution_enabled
    for (const p of res.policies) draft[p.category] = { enabled: p.enabled, months: p.keep_months || undefined }
    await Promise.all([loadHolds(), loadHistory()])
  } catch {
    toast.error(t('teamChat.errors.generic'))
  } finally {
    loading.value = false
  }
}
async function loadHolds() {
  holds.value = unwrap<{ holds: RetentionHold[] }>(await retentionService.holds('active')).holds
}
async function loadHistory() {
  history.value = unwrap<{ history: typeof history.value }>(await retentionService.history()).history
}

const saved = (c: string) => policies.value.find(p => p.category === c)
const dirty = (c: string) => {
  const s = saved(c)
  const d = draft[c]
  return !!d && (d.enabled !== (s?.enabled ?? false) || (d.months ?? 0) !== (s?.keep_months ?? 0))
}
const canEnable = (c: string) => (draft[c]?.months ?? 0) >= 1

async function save(c: string) {
  const d = draft[c]
  if (d.enabled && !canEnable(c)) {
    toast.error(t('retention.needTerm'))
    return
  }
  saving.value = c
  try {
    await retentionService.putPolicy(c, { keep_months: d.months ?? 0, enabled: d.enabled })
    toast.success(t('retention.saved'))
    await load()
  } catch {
    toast.error(t('teamChat.errors.generic'))
  } finally {
    saving.value = null
  }
}

// The simulation uses the term typed in each row (saved or not): "what would this term do?".
async function simulate() {
  simulating.value = true
  try {
    const months: Record<string, number> = {}
    for (const c of Object.keys(draft)) if ((draft[c].months ?? 0) >= 1) months[c] = draft[c].months as number
    const res = unwrap<{ generated_at: string; categories: RetentionCategoryReport[] }>(await retentionService.simulate({ months }))
    simulation.value = res.categories
    simulatedAt.value = res.generated_at
  } catch {
    toast.error(t('teamChat.errors.generic'))
  } finally {
    simulating.value = false
  }
}

const count = (r: RetentionCategoryReport, b: string) => r.buckets?.[b]?.count ?? 0
const gb = (n: number) => (n >= 1 << 30 ? `${(n / (1 << 30)).toFixed(2)} GB` : `${(n / (1 << 20)).toFixed(1)} MB`)

async function placeHold() {
  holdBusy.value = true
  try {
    await retentionService.createHold({ scope_type: 'occurrence', protocol_number: holdProtocol.value.trim(), reason: holdReason.value.trim() })
    toast.success(t('retention.holdPlaced'))
    holdDialog.value = false
    holdProtocol.value = ''
    holdReason.value = ''
    await Promise.all([loadHolds(), loadHistory()])
  } catch (e) {
    const status = (e as { response?: { status?: number } }).response?.status
    toast.error(status === 404 ? t('retention.protocolNotFound') : status === 409 ? t('retention.holdExists') : t('teamChat.errors.generic'))
  } finally {
    holdBusy.value = false
  }
}
async function revoke() {
  if (!revokeTarget.value) return
  holdBusy.value = true
  try {
    await retentionService.revokeHold(revokeTarget.value.id, revokeReason.value.trim())
    toast.success(t('retention.holdRevoked'))
    revokeTarget.value = null
    revokeReason.value = ''
    await Promise.all([loadHolds(), loadHistory()])
  } catch {
    toast.error(t('teamChat.errors.generic'))
  } finally {
    holdBusy.value = false
  }
}

const dialogOpen = computed({ get: () => !!revokeTarget.value, set: (v: boolean) => { if (!v) revokeTarget.value = null } })

onMounted(load)
</script>

<template>
  <div class="mx-auto max-w-5xl space-y-6 p-4 sm:p-6" data-testid="retention-settings">
    <Card>
      <CardHeader>
        <CardTitle class="flex items-center gap-2 text-base"><Archive class="h-4 w-4" aria-hidden="true" />{{ t('retention.title') }}</CardTitle>
        <CardDescription>{{ t('retention.description') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-6">
        <div v-if="loading" class="flex justify-center py-6"><Loader2 class="h-5 w-5 animate-spin text-muted-foreground" aria-hidden="true" /></div>
        <template v-else>
          <!-- Always visible: nothing is deleted by this version -->
          <div class="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/5 p-3 text-sm" role="status" data-testid="retention-engine-off">
            <Lock class="mt-0.5 h-4 w-4 shrink-0 text-amber-600" aria-hidden="true" />
            <p>{{ executionEnabled ? t('retention.engineOn') : t('retention.engineOff') }}</p>
          </div>

          <!-- Policies -->
          <section aria-labelledby="ret-rules">
            <h3 id="ret-rules" class="mb-1 text-sm font-medium">{{ t('retention.rules') }}</h3>
            <p class="mb-3 text-xs text-muted-foreground">{{ t('retention.rulesHint') }}</p>
            <ul class="divide-y rounded-md border">
              <li v-for="p in policies" :key="p.category" class="grid items-center gap-3 p-3 sm:grid-cols-[1fr_auto_auto_auto]" :data-testid="`retention-row-${p.category}`">
                <div class="min-w-0">
                  <p class="text-sm font-medium">{{ t(`retention.categories.${p.category}.name`) }}</p>
                  <p class="text-xs text-muted-foreground">{{ t(`retention.categories.${p.category}.rule`) }}</p>
                  <p v-if="p.updated_by" class="mt-0.5 text-[11px] text-muted-foreground">{{ t('retention.updatedBy', { name: p.updated_by, date: formatDateTime(p.updated_at as string) }) }}</p>
                </div>
                <div class="flex items-center gap-2">
                  <Switch :id="`ret-on-${p.category}`" :checked="draft[p.category].enabled" :aria-label="t('retention.enable', { name: t(`retention.categories.${p.category}.name`) })"
                          @update:checked="(v: boolean) => (draft[p.category].enabled = v)" />
                  <Label :for="`ret-on-${p.category}`" class="text-xs">{{ draft[p.category].enabled ? t('retention.on') : t('retention.off') }}</Label>
                </div>
                <div class="flex items-center gap-2">
                  <Label :for="`ret-m-${p.category}`" class="text-xs text-muted-foreground">{{ t('retention.months') }}</Label>
                  <Input :id="`ret-m-${p.category}`" v-model.number="draft[p.category].months" type="number" min="1" max="600" inputmode="numeric" class="h-10 w-20" />
                </div>
                <Button size="sm" variant="outline" class="min-h-10" :disabled="!dirty(p.category) || saving === p.category" :data-testid="`retention-save-${p.category}`" @click="save(p.category)">
                  <Loader2 v-if="saving === p.category" class="mr-1.5 h-4 w-4 animate-spin" aria-hidden="true" />{{ t('retention.save') }}
                </Button>
              </li>
            </ul>
          </section>

          <!-- Simulation (read-only) -->
          <section aria-labelledby="ret-sim">
            <div class="mb-2 flex flex-wrap items-center gap-3">
              <h3 id="ret-sim" class="text-sm font-medium">{{ t('retention.simulation') }}</h3>
              <Button size="sm" class="min-h-10" :disabled="simulating" data-testid="retention-simulate" @click="simulate">
                <Loader2 v-if="simulating" class="mr-1.5 h-4 w-4 animate-spin" aria-hidden="true" /><Play v-else class="mr-1.5 h-4 w-4" aria-hidden="true" />{{ t('retention.simulate') }}
              </Button>
              <span class="text-xs text-muted-foreground">{{ t('retention.simulateHint') }}</span>
            </div>
            <div v-if="simulation" class="overflow-x-auto" data-testid="retention-result">
              <p class="mb-2 text-xs text-muted-foreground">{{ t('retention.generatedAt', { date: formatDateTime(simulatedAt) }) }}</p>
              <table class="w-full text-sm">
                <thead>
                  <tr class="border-b text-left text-xs text-muted-foreground">
                    <th class="py-1.5 pr-3 font-medium">{{ t('retention.col.category') }}</th>
                    <th class="px-2 py-1.5 text-right font-medium">{{ t('retention.col.eligible') }}</th>
                    <th class="px-2 py-1.5 text-right font-medium">{{ t('retention.col.volume') }}</th>
                    <th class="px-2 py-1.5 text-right font-medium">{{ t('retention.col.notDue') }}</th>
                    <th class="px-2 py-1.5 text-right font-medium">{{ t('retention.col.open') }}</th>
                    <th class="px-2 py-1.5 text-right font-medium">{{ t('retention.col.held') }}</th>
                    <th class="px-2 py-1.5 text-right font-medium">{{ t('retention.col.shared') }}</th>
                    <th class="px-2 py-1.5 text-right font-medium">{{ t('retention.col.quarantine') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <template v-for="r in simulation" :key="r.category">
                    <tr class="border-b" :data-testid="`retention-sim-${r.category}`">
                      <td class="py-1.5 pr-3">
                        <button type="button" class="text-left underline-offset-2 hover:underline focus-visible:underline" :aria-expanded="openSamples === r.category"
                                @click="openSamples = openSamples === r.category ? null : r.category">{{ t(`retention.categories.${r.category}.name`) }}</button>
                        <p v-if="!r.applied" class="text-[11px] text-muted-foreground">{{ t(`retention.reason.${r.reason}`) }}</p>
                        <p v-else class="text-[11px] text-muted-foreground">{{ t('retention.appliedTerm', { n: r.months }) }}<span v-if="r.assumed"> · {{ t('retention.assumed') }}</span></p>
                      </td>
                      <template v-if="r.applied">
                        <td class="px-2 py-1.5 text-right font-medium tabular-nums">{{ count(r, 'eligible') }}</td>
                        <td class="px-2 py-1.5 text-right tabular-nums">{{ gb(r.buckets.eligible.bytes) }}<span v-if="r.bytes_partial">*</span></td>
                        <td class="px-2 py-1.5 text-right tabular-nums">{{ count(r, 'not_due') }}</td>
                        <td class="px-2 py-1.5 text-right tabular-nums">{{ count(r, 'open') }}</td>
                        <td class="px-2 py-1.5 text-right tabular-nums">{{ count(r, 'held') }}</td>
                        <td class="px-2 py-1.5 text-right tabular-nums">{{ count(r, 'shared') }}</td>
                        <td class="px-2 py-1.5 text-right tabular-nums">{{ count(r, 'quarantine') }}</td>
                      </template>
                      <td v-else colspan="7" class="px-2 py-1.5 text-right text-xs text-muted-foreground">—</td>
                    </tr>
                    <tr v-if="openSamples === r.category && r.applied">
                      <td colspan="8" class="bg-muted/30 px-3 py-2">
                        <p v-if="!r.samples.length && !r.protected_samples.length" class="text-xs text-muted-foreground">{{ t('retention.noSamples') }}</p>
                        <ul class="space-y-1 text-xs">
                          <li v-for="s in [...r.samples, ...r.protected_samples]" :key="s.id" class="flex flex-wrap items-center gap-x-3">
                            <Badge :variant="s.bucket === 'eligible' ? 'default' : 'secondary'">{{ t(`retention.bucket.${s.bucket}`) }}</Badge>
                            <span class="font-medium">{{ s.name || s.id.slice(0, 8) }}</span>
                            <span v-if="s.related" class="text-muted-foreground">{{ s.related }}</span>
                            <span class="text-muted-foreground">{{ s.reason }}</span>
                          </li>
                        </ul>
                      </td>
                    </tr>
                  </template>
                </tbody>
              </table>
              <p class="mt-2 text-[11px] text-muted-foreground">{{ t('retention.volumeNote') }}</p>
            </div>
          </section>

          <!-- Holds -->
          <section aria-labelledby="ret-holds">
            <div class="mb-2 flex flex-wrap items-center gap-3">
              <h3 id="ret-holds" class="flex items-center gap-2 text-sm font-medium"><Gavel class="h-4 w-4" aria-hidden="true" />{{ t('retention.holds') }}</h3>
              <Button size="sm" variant="outline" class="min-h-10" data-testid="retention-new-hold" @click="holdDialog = true">{{ t('retention.newHold') }}</Button>
            </div>
            <p class="mb-2 text-xs text-muted-foreground">{{ t('retention.holdsHint') }}</p>
            <p v-if="!holds.length" class="rounded-md border border-dashed p-3 text-sm text-muted-foreground">{{ t('retention.noHolds') }}</p>
            <ul v-else class="divide-y rounded-md border" data-testid="retention-holds">
              <li v-for="h in holds" :key="h.id" class="flex flex-wrap items-start gap-3 p-3">
                <div class="min-w-0 flex-1">
                  <p class="text-sm font-medium">{{ t(`retention.scope.${h.scope_type}`) }} {{ h.scope_label }}</p>
                  <p class="text-xs text-muted-foreground">{{ h.reason }}</p>
                  <p class="mt-0.5 text-[11px] text-muted-foreground">{{ t('retention.placedBy', { name: h.created_by, date: formatDateTime(h.created_at) }) }}</p>
                </div>
                <Button size="sm" variant="outline" class="min-h-10" @click="revokeTarget = h">{{ t('retention.revoke') }}</Button>
              </li>
            </ul>
          </section>

          <!-- History -->
          <section aria-labelledby="ret-history">
            <h3 id="ret-history" class="mb-2 text-sm font-medium">{{ t('retention.history') }}</h3>
            <p v-if="!history.length" class="text-sm text-muted-foreground">{{ t('retention.noHistory') }}</p>
            <ul v-else class="divide-y rounded-md border text-sm" data-testid="retention-history">
              <li v-for="(h, i) in history" :key="i" class="flex flex-wrap items-baseline gap-x-3 px-3 py-2">
                <span class="text-xs text-muted-foreground">{{ formatDateTime(h.at) }}</span>
                <Badge variant="secondary">{{ t(`retention.kind.${h.kind}`) }}</Badge>
                <span class="min-w-0 flex-1">{{ h.summary }}</span>
                <span v-if="h.actor" class="text-xs text-muted-foreground">{{ h.actor }}</span>
              </li>
            </ul>
          </section>

          <section aria-labelledby="ret-out">
            <h3 id="ret-out" class="mb-1 text-sm font-medium">{{ t('retention.notCovered') }}</h3>
            <ul class="list-disc space-y-0.5 pl-5 text-xs text-muted-foreground">
              <li v-for="k in notCovered" :key="k">{{ t(`retention.out.${k}`) }}</li>
            </ul>
          </section>
        </template>
      </CardContent>
    </Card>

    <!-- New hold -->
    <Dialog v-model:open="holdDialog">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{{ t('retention.newHold') }}</DialogTitle>
          <DialogDescription>{{ t('retention.newHoldHint') }}</DialogDescription>
        </DialogHeader>
        <div class="space-y-3">
          <div class="space-y-1.5">
            <Label for="hold-protocol">{{ t('retention.protocol') }}</Label>
            <Input id="hold-protocol" v-model="holdProtocol" placeholder="2026-000123" autocomplete="off" />
          </div>
          <div class="space-y-1.5">
            <Label for="hold-reason">{{ t('retention.reasonLabel') }}</Label>
            <Textarea id="hold-reason" v-model="holdReason" :rows="3" :aria-describedby="'hold-reason-help'" />
            <p id="hold-reason-help" class="text-xs" :class="reasonOk(holdReason) || !holdReason ? 'text-muted-foreground' : 'text-destructive'">{{ t('retention.reasonHelp', { n: MIN_REASON }) }}</p>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" @click="holdDialog = false">{{ t('common.cancel') }}</Button>
          <Button :disabled="holdBusy || !holdProtocol.trim() || !reasonOk(holdReason)" data-testid="retention-confirm-hold" @click="placeHold">
            <Loader2 v-if="holdBusy" class="mr-1.5 h-4 w-4 animate-spin" aria-hidden="true" />{{ t('retention.placeHold') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- Revoke -->
    <Dialog v-model:open="dialogOpen">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{{ t('retention.revokeTitle') }}</DialogTitle>
          <DialogDescription>{{ revokeTarget ? `${t(`retention.scope.${revokeTarget.scope_type}`)} ${revokeTarget.scope_label}` : '' }}</DialogDescription>
        </DialogHeader>
        <div class="space-y-1.5">
          <Label for="revoke-reason">{{ t('retention.reasonLabel') }}</Label>
          <Textarea id="revoke-reason" v-model="revokeReason" :rows="3" />
          <p class="text-xs" :class="reasonOk(revokeReason) || !revokeReason ? 'text-muted-foreground' : 'text-destructive'">{{ t('retention.reasonHelp', { n: MIN_REASON }) }}</p>
        </div>
        <DialogFooter>
          <Button variant="outline" @click="revokeTarget = null">{{ t('common.cancel') }}</Button>
          <Button :disabled="holdBusy || !reasonOk(revokeReason)" data-testid="retention-confirm-revoke" @click="revoke">{{ t('retention.revoke') }}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
