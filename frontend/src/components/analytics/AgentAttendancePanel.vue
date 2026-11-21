<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { agentAttendanceService, teamsService, unitsService, rolesService } from '@/services/api'

interface Session { session_id: string; login_at: string; end_at?: string; ended: 'manual' | 'expired' | 'active'; minutes: number; user_agent?: string }
interface Interval { start_at: string; end_at?: string; minutes: number; closed_reason?: string }
interface Row {
  user_id: string; name: string; no_login_record: boolean; logins: number
  first_login?: string; last_logout?: string; active_sessions: number; connected_now: boolean
  connected_minutes: number; away_minutes: number; away_count: number; open_away: boolean
  disconnections: number; sessions: Session[]; away: Interval[]
}

const props = defineProps<{ from: string; to: string }>()
const { t } = useI18n()

const rows = ref<Row[]>([])
const failed = ref(false)
const expanded = ref<string | null>(null)
const f = ref({ team_id: '', unit_id: '', role_id: '', only: '' })
const teams = ref<{ id: string; name: string }[]>([])
const units = ref<{ id: string; name: string }[]>([])
const roles = ref<{ id: string; name: string }[]>([])

const fmt = (iso?: string) => (iso ? new Date(iso).toLocaleString('pt-BR', { timeZone: 'America/Sao_Paulo' }) : '—')
const hm = (m: number) => (m >= 60 ? `${Math.floor(m / 60)}h ${m % 60}m` : `${m}m`)
const endedLabel = (s: Session) => t(`agentAnalytics.attendance.ended_${s.ended}`)
const device = (ua?: string) => (!ua ? '' : /Mobile|Android|iPhone/i.test(ua) ? t('agentAnalytics.attendance.mobile') : t('agentAnalytics.attendance.desktop'))
const noLoginCount = computed(() => rows.value.filter(r => r.no_login_record).length)

async function load() {
  try {
    const params: Record<string, string> = { from: props.from, to: props.to }
    for (const [k, v] of Object.entries(f.value)) if (v) params[k] = v
    const res = await agentAttendanceService.get(params)
    rows.value = (res.data.data || res.data).agents || []
    failed.value = false
  } catch {
    failed.value = true
    rows.value = []
  }
}

onMounted(async () => {
  load()
  // The filter lists are best effort: a role that cannot read them still gets the report.
  teamsService.list({ limit: 100 }).then(r => { teams.value = (r.data as any).data?.teams ?? (r.data as any).teams ?? [] }).catch(() => {})
  unitsService.list().then(r => { units.value = r.data.data.units }).catch(() => {})
  rolesService.list({ limit: 100 }).then(r => { roles.value = (r.data as any).data?.roles ?? (r.data as any).roles ?? [] }).catch(() => {})
})
watch([() => props.from, () => props.to, f], load, { deep: true })
</script>

<template>
  <Card data-testid="attendance-panel">
    <CardHeader>
      <CardTitle>{{ $t('agentAnalytics.attendance.title') }}</CardTitle>
      <CardDescription>{{ $t('agentAnalytics.attendance.description') }}</CardDescription>
      <div class="flex flex-wrap gap-2 pt-2 text-sm">
        <select v-model="f.team_id" class="h-8 rounded-md border bg-transparent px-2" :aria-label="$t('agentAnalytics.attendance.team')">
          <option value="">{{ $t('agentAnalytics.attendance.allTeams') }}</option>
          <option v-for="x in teams" :key="x.id" :value="x.id" class="text-black">{{ x.name }}</option>
        </select>
        <select v-model="f.unit_id" class="h-8 rounded-md border bg-transparent px-2" :aria-label="$t('agentAnalytics.attendance.unit')">
          <option value="">{{ $t('agentAnalytics.attendance.allUnits') }}</option>
          <option v-for="x in units" :key="x.id" :value="x.id" class="text-black">{{ x.name }}</option>
        </select>
        <select v-model="f.role_id" class="h-8 rounded-md border bg-transparent px-2" :aria-label="$t('agentAnalytics.attendance.role')">
          <option value="">{{ $t('agentAnalytics.attendance.allRoles') }}</option>
          <option v-for="x in roles" :key="x.id" :value="x.id" class="text-black">{{ x.name }}</option>
        </select>
        <label class="flex items-center gap-1.5">
          <input v-model="f.only" type="checkbox" true-value="no_login" false-value="" data-testid="only-no-login" />
          {{ $t('agentAnalytics.attendance.onlyNoLogin') }}
        </label>
      </div>
    </CardHeader>
    <CardContent class="overflow-x-auto">
      <p v-if="failed" class="text-sm text-muted-foreground">{{ $t('agentAnalytics.attendance.loadFailed') }}</p>
      <template v-else>
        <p v-if="noLoginCount && !f.only" class="mb-2 text-xs text-muted-foreground">
          {{ $t('agentAnalytics.attendance.noLoginHint', { n: noLoginCount }) }}
        </p>
        <table class="w-full text-sm">
          <thead>
            <tr class="text-left text-muted-foreground">
              <th class="py-2 pr-4">{{ $t('agentAnalytics.attendance.agent') }}</th>
              <th class="py-2 pr-4">{{ $t('agentAnalytics.attendance.firstLogin') }}</th>
              <th class="py-2 pr-4">{{ $t('agentAnalytics.attendance.logins') }}</th>
              <th class="py-2 pr-4">{{ $t('agentAnalytics.attendance.connected') }}</th>
              <th class="py-2 pr-4">{{ $t('agentAnalytics.attendance.away') }}</th>
              <th class="py-2">{{ $t('agentAnalytics.attendance.lastLogout') }}</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="r in rows" :key="r.user_id">
              <tr class="cursor-pointer border-t border-border" data-testid="attendance-row" @click="expanded = expanded === r.user_id ? null : r.user_id">
                <td class="py-2 pr-4">
                  <span v-if="r.connected_now" class="mr-1.5 inline-block h-2 w-2 rounded-full bg-emerald-500" :title="$t('agentAnalytics.attendance.connectedNow')" data-testid="connected-now" />{{ r.name }}
                </td>
                <template v-if="r.no_login_record">
                  <td class="py-2 pr-4 text-muted-foreground" colspan="2" data-testid="no-login">{{ $t('agentAnalytics.attendance.noLogin') }}</td>
                </template>
                <template v-else>
                  <td class="py-2 pr-4">{{ fmt(r.first_login) }}</td>
                  <td class="py-2 pr-4">{{ r.logins }}<span v-if="r.active_sessions" class="ml-1 text-xs text-emerald-500">({{ $t('agentAnalytics.attendance.activeN', { n: r.active_sessions }) }})</span></td>
                </template>
                <td class="py-2 pr-4">{{ hm(r.connected_minutes) }}</td>
                <td class="py-2 pr-4">
                  {{ hm(r.away_minutes) }} ({{ r.away_count }}x)
                  <span v-if="r.open_away" class="ml-1 text-xs text-amber-500">{{ $t('agentAnalytics.attendance.noReturn') }}</span>
                </td>
                <td class="py-2">{{ fmt(r.last_logout) }}</td>
              </tr>
              <tr v-if="expanded === r.user_id" class="bg-muted/30">
                <td colspan="6" class="p-3 text-xs">
                  <p class="mb-1 font-medium">{{ $t('agentAnalytics.attendance.sessions') }}</p>
                  <ul class="mb-2 space-y-0.5">
                    <li v-for="s in r.sessions" :key="s.session_id">
                      {{ fmt(s.login_at) }} → {{ s.ended === 'active' ? $t('agentAnalytics.attendance.ended_active') : fmt(s.end_at) }}
                      · {{ hm(s.minutes) }} · {{ endedLabel(s) }} <span v-if="device(s.user_agent)">· {{ device(s.user_agent) }}</span>
                    </li>
                    <li v-if="!r.sessions.length" class="text-muted-foreground">—</li>
                  </ul>
                  <p class="mb-1 font-medium">{{ $t('agentAnalytics.attendance.awayPeriods') }}</p>
                  <ul class="space-y-0.5">
                    <li v-for="a in r.away" :key="a.start_at">
                      {{ fmt(a.start_at) }} → {{ a.end_at ? fmt(a.end_at) : $t('agentAnalytics.attendance.noReturn') }} · {{ hm(a.minutes) }}
                    </li>
                    <li v-if="!r.away.length" class="text-muted-foreground">—</li>
                  </ul>
                  <p v-if="r.disconnections" class="mt-2 text-muted-foreground">{{ $t('agentAnalytics.attendance.disconnections', { n: r.disconnections }) }}</p>
                </td>
              </tr>
            </template>
            <tr v-if="!rows.length"><td colspan="6" class="py-4 text-muted-foreground">{{ $t('agentAnalytics.attendance.empty') }}</td></tr>
          </tbody>
        </table>
      </template>
    </CardContent>
  </Card>
</template>
