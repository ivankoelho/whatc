<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Bell, X } from 'lucide-vue-next'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { useAuthStore } from '@/stores/auth'
import { occurrenceAlertsService, type OccurrenceAlert } from '@/services/api'

// Protocol alerts bell for the shell header (the clock is HeaderClock).
const router = useRouter()
const { t } = useI18n()
const authStore = useAuthStore()

// Alerts live on the server (per-user dismissal). The WebSocket pushes a
// refetch hint; the 60s poll is only the fallback while the socket is down.
// 'now' only feeds the 'time since opened' labels; it is refreshed on every load.
const now = ref(new Date())
const items = ref<OccurrenceAlert[]>([])
const unseen = ref(0)
const isOpen = ref(false)
const canSee = computed(() => authStore.hasPermission('occurrences', 'read'))

async function load() {
  if (!canSee.value) return
  now.value = new Date()
  try {
    const res = await occurrenceAlertsService.list()
    items.value = res.data.data.alerts
    unseen.value = res.data.data.unseen
  } catch { /* best effort */ }
}

async function onOpenChange(open: boolean) {
  isOpen.value = open
  if (open) { await load(); return }
  // Closing the list is what marks it seen: new items stay highlighted while visible.
  if (unseen.value > 0) {
    try { await occurrenceAlertsService.markSeen() } catch { /* ignore */ }
    await load()
  }
}
async function dismiss(id: string) {
  items.value = items.value.filter(n => n.id !== id)
  try { await occurrenceAlertsService.dismiss(id) } catch { await load() }
}
async function dismissAll() {
  items.value = []
  try { await occurrenceAlertsService.dismissAll() } catch { await load() }
}
function open(n: OccurrenceAlert) {
  isOpen.value = false
  router.push(`/crm/occurrences/${n.occurrence_id}`)
}

const category = (n: OccurrenceAlert) => n.what_happened || n.category_name || t('occurrences.alerts.uncategorized')
const PRIORITY: Record<string, string> = { low: 'priorityLow', normal: 'priorityNormal', high: 'priorityHigh', urgent: 'priorityUrgent' }
function since(iso: string) {
  const m = Math.max(0, Math.floor((now.value.getTime() - new Date(iso).getTime()) / 60000))
  if (m < 60) return `${m} min`
  if (m < 1440) return `${Math.floor(m / 60)} h`
  return `${Math.floor(m / 1440)} d`
}

let poll: number
const onPush = () => load()
onMounted(() => {
  poll = window.setInterval(load, 60_000)
  window.addEventListener('occurrence-alert', onPush)
  load()
})
onUnmounted(() => {
  clearInterval(poll)
  window.removeEventListener('occurrence-alert', onPush)
})
</script>

<template>
  <div class="flex shrink-0 items-center text-white" data-testid="header-tools">
    <Popover v-if="canSee" :open="isOpen" @update:open="onOpenChange">
      <PopoverTrigger as-child>
        <button type="button" data-testid="bell" class="shell-chip relative h-9 w-9 justify-center" :aria-label="$t('occurrences.alerts.title')">
          <Bell class="h-4 w-4" aria-hidden="true" />
          <span v-if="unseen" class="absolute -right-0.5 -top-0.5 min-w-4 rounded-full bg-red-500 px-1 text-center text-[10px] leading-4 text-white">{{ unseen }}</span>
        </button>
      </PopoverTrigger>
      <PopoverContent align="end" class="w-[min(24rem,calc(100vw-1rem))] p-0 text-foreground">
        <p v-if="!items.length" class="p-4 text-sm text-muted-foreground">{{ $t('occurrences.alerts.empty') }}</p>
        <ul v-else class="max-h-96 divide-y overflow-y-auto">
          <li v-for="n in items" :key="n.id" data-testid="alert" :class="['flex items-start gap-2 p-3', !n.seen_at && 'bg-emerald-500/10']">
            <button type="button" class="min-w-0 flex-1 text-left" @click="open(n)">
              <span class="flex items-center gap-1.5 text-xs text-muted-foreground">
                <span v-if="!n.seen_at" class="h-1.5 w-1.5 rounded-full bg-emerald-500" aria-hidden="true" />
                <span class="whitespace-nowrap font-mono">{{ n.protocol_number }}</span> · {{ $t('occurrences.alerts.kind_' + n.kind) }} · {{ since(n.opened_at) }}
              </span>
              <span class="block truncate text-sm">{{ n.title }}</span>
              <span class="block truncate text-xs text-muted-foreground">{{ n.contact_name }} · {{ category(n) }} · {{ $t('occurrences.' + PRIORITY[n.priority]) }}</span>
            </button>
            <button type="button" class="p-1 text-muted-foreground hover:text-foreground" :aria-label="$t('occurrences.alerts.dismiss')" @click="dismiss(n.id)">
              <X class="h-3.5 w-3.5" />
            </button>
          </li>
        </ul>
        <button v-if="items.length" type="button" class="w-full border-t p-2 text-xs text-muted-foreground hover:text-foreground" @click="dismissAll">
          {{ $t('occurrences.alerts.dismissAll') }}
        </button>
      </PopoverContent>
    </Popover>
  </div>
</template>
