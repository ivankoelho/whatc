<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { agentAnalyticsService } from '@/services/api'

interface LiveAgent {
  agent_id: string
  agent_name: string
  state: 'online' | 'away' | 'offline'
  state_since?: string
  active_transfers: number
  awaiting_agent: number
  awaiting_customer: number
  sla_at_risk: number
}
interface LiveResponse {
  scope: 'all' | 'team'
  agents: LiveAgent[]
  queue: number
}

const REFRESH_MS = 15000

const { t } = useI18n()
const live = ref<LiveResponse | null>(null)
const failed = ref(false)
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | undefined

async function load() {
  try {
    const res = await agentAnalyticsService.getLive()
    live.value = (res.data as { data: LiveResponse }).data
    failed.value = false
  } catch {
    failed.value = true
  }
  now.value = Date.now()
}

onMounted(() => {
  load()
  timer = setInterval(load, REFRESH_MS)
})
onUnmounted(() => clearInterval(timer))

const stateClass: Record<LiveAgent['state'], string> = {
  online: 'bg-emerald-500/20 text-emerald-400',
  away: 'bg-amber-500/20 text-amber-400',
  offline: 'bg-white/10 text-white/50 light:bg-gray-100 light:text-gray-500'
}

// "12 min" / "2 h 5 min"; empty when the log cannot tell when the state began.
function since(iso?: string): string {
  if (!iso) return ''
  const mins = Math.max(0, Math.floor((now.value - new Date(iso).getTime()) / 60000))
  return mins < 60 ? `${mins} min` : `${Math.floor(mins / 60)} h ${mins % 60} min`
}

const counts = computed(() => {
  const agents = live.value?.agents ?? []
  const n = (s: LiveAgent['state']) => agents.filter(a => a.state === s).length
  return { online: n('online'), away: n('away'), offline: n('offline') }
})
</script>

<template>
  <div
    v-if="live || failed"
    class="card-depth rounded-xl border border-white/[0.08] bg-white/[0.04] p-6 light:bg-white light:border-gray-200"
  >
    <div class="flex flex-wrap items-center justify-between gap-2 mb-4">
      <div>
        <h2 class="text-base font-semibold text-white light:text-gray-900">{{ t('agentAnalytics.live.title') }}</h2>
        <p class="text-xs text-white/40 light:text-gray-500">
          {{ live?.scope === 'team' ? t('agentAnalytics.live.scopeTeam') : t('agentAnalytics.live.scopeAll') }}
        </p>
      </div>
      <p v-if="failed" class="text-xs text-red-400">{{ t('agentAnalytics.live.loadFailed') }}</p>
      <div v-if="live" class="flex flex-wrap items-center gap-3 text-xs text-white/60 light:text-gray-600">
        <span>{{ t('agentAnalytics.live.online') }}: <b>{{ counts.online }}</b></span>
        <span>{{ t('agentAnalytics.live.away') }}: <b>{{ counts.away }}</b></span>
        <span>{{ t('agentAnalytics.live.offline') }}: <b>{{ counts.offline }}</b></span>
        <span>{{ t('agentAnalytics.live.queue') }}: <b>{{ live.queue }}</b></span>
      </div>
    </div>

    <p v-if="live && !live.agents.length" class="text-sm text-white/50 light:text-gray-500">
      {{ t('agentAnalytics.live.empty') }}
    </p>
    <div v-else-if="live" class="overflow-x-auto">
      <table class="w-full text-sm">
        <thead>
          <tr class="text-left text-xs text-white/40 light:text-gray-500">
            <th class="py-2 pr-4 font-medium">{{ t('agentAnalytics.live.agent') }}</th>
            <th class="py-2 pr-4 font-medium">{{ t('agentAnalytics.live.state') }}</th>
            <th class="py-2 pr-4 font-medium text-right">{{ t('agentAnalytics.live.active') }}</th>
            <th class="py-2 pr-4 font-medium text-right">{{ t('agentAnalytics.live.awaitingAgent') }}</th>
            <th class="py-2 pr-4 font-medium text-right">{{ t('agentAnalytics.live.awaitingCustomer') }}</th>
            <th class="py-2 font-medium text-right">{{ t('agentAnalytics.live.slaAtRisk') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="a in live.agents"
            :key="a.agent_id"
            class="border-t border-white/[0.06] text-white light:text-gray-900 light:border-gray-100"
          >
            <td class="py-2 pr-4">{{ a.agent_name }}</td>
            <td class="py-2 pr-4">
              <span class="rounded-full px-2 py-0.5 text-xs" :class="stateClass[a.state]">
                {{ t(`agentAnalytics.live.states.${a.state}`) }}
              </span>
              <span v-if="since(a.state_since)" class="ml-2 text-xs text-white/40 light:text-gray-500">
                {{ since(a.state_since) }}
              </span>
            </td>
            <td class="py-2 pr-4 text-right">{{ a.active_transfers }}</td>
            <td class="py-2 pr-4 text-right" :class="a.awaiting_agent ? 'font-semibold text-amber-400' : ''">{{ a.awaiting_agent }}</td>
            <td class="py-2 pr-4 text-right">{{ a.awaiting_customer }}</td>
            <td class="py-2 text-right" :class="a.sla_at_risk ? 'font-semibold text-red-400' : ''">{{ a.sla_at_risk }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
