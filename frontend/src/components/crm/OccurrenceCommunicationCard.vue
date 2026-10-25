<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { occurrenceNotificationsService, type OccurrenceNotification } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDateTime } from '@/lib/utils'
import { MessageSquareText } from 'lucide-vue-next'

const props = defineProps<{ occurrenceId: string }>()

const { t, te } = useI18n()
const items = ref<OccurrenceNotification[]>([])
const loaded = ref(false)
const isBusy = ref(false)
let timer: ReturnType<typeof setInterval> | null = null

// States the provider or the worker may still change: keep the card fresh.
const LIVE = ['pending', 'processing', 'accepted', 'delivered']
const hasLive = computed(() => items.value.some(n => LIVE.includes(n.status)))

const tone: Record<string, 'default' | 'secondary' | 'destructive' | 'outline'> = {
  read: 'default', delivered: 'default', accepted: 'secondary', pending: 'secondary', processing: 'secondary',
  prepared: 'outline', skipped: 'outline', cancelled: 'outline', failed: 'destructive', uncertain: 'destructive',
}

function statusLabel(s: string) {
  return te(`occurrences.notice.${s}`) ? t(`occurrences.notice.${s}`) : s
}
function reasonLabel(r?: string) {
  if (!r) return ''
  return te(`occurrences.noticeReason.${r}`) ? t(`occurrences.noticeReason.${r}`) : r
}

async function load() {
  try {
    const res = await occurrenceNotificationsService.list(props.occurrenceId)
    const body = res.data as any
    items.value = (body.data ?? body).notifications ?? []
  } catch {
    items.value = []
  } finally {
    loaded.value = true
  }
}

async function act(n: OccurrenceNotification, action: 'retry' | 'cancel') {
  let confirmRisk = false
  if (action === 'retry' && n.status === 'uncertain') {
    // The provider may already have accepted it: a second message can reach the customer.
    if (!window.confirm(t('occurrences.noticeDuplicateConfirm'))) return
    confirmRisk = true
  }
  isBusy.value = true
  try {
    await occurrenceNotificationsService.resolve(props.occurrenceId, n.id, action, confirmRisk)
    toast.success(t(action === 'retry' ? 'occurrences.noticeRetryQueued' : 'occurrences.noticeCancelled'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.noticeActionFailed')))
  } finally {
    isBusy.value = false
    await load()
  }
}

function restartTimer() {
  if (timer) clearInterval(timer)
  timer = hasLive.value ? setInterval(load, 5000) : null
}
watch(hasLive, restartTimer)
watch(() => props.occurrenceId, load, { immediate: true })
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <Card v-if="loaded && items.length > 0" data-testid="occurrence-communication">
    <CardHeader class="pb-3">
      <CardTitle class="text-sm font-medium flex items-center gap-2">
        <MessageSquareText class="h-4 w-4" aria-hidden="true" />
        {{ $t('occurrences.communication') }}
      </CardTitle>
      <p class="text-xs text-muted-foreground">{{ $t('occurrences.communicationHint') }}</p>
    </CardHeader>
    <CardContent class="space-y-4 text-sm">
      <div v-for="n in items" :key="n.id" class="space-y-2" :data-testid="`notification-${n.status}`">
        <div class="flex flex-wrap items-center gap-2">
          <Badge :variant="tone[n.status] || 'outline'" data-testid="notification-status">{{ statusLabel(n.status) }}</Badge>
          <span v-if="n.reason" class="text-xs text-muted-foreground">{{ reasonLabel(n.reason) }}</span>
        </div>
        <dl class="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
          <div v-if="n.whatsapp_account"><dt class="text-muted-foreground">{{ $t('occurrences.noticeAccount') }}</dt><dd>{{ n.whatsapp_account }}</dd></div>
          <div v-if="n.template_name"><dt class="text-muted-foreground">{{ $t('occurrences.noticeTemplate') }}</dt><dd>{{ n.template_name }} ({{ n.template_language }})</dd></div>
          <div v-if="n.accepted_at"><dt class="text-muted-foreground">{{ $t('occurrences.noticeAcceptedAt') }}</dt><dd>{{ formatDateTime(n.accepted_at) }}</dd></div>
          <div v-if="n.delivered_at"><dt class="text-muted-foreground">{{ $t('occurrences.noticeDeliveredAt') }}</dt><dd>{{ formatDateTime(n.delivered_at) }}</dd></div>
          <div v-if="n.read_at"><dt class="text-muted-foreground">{{ $t('occurrences.noticeReadAt') }}</dt><dd>{{ formatDateTime(n.read_at) }}</dd></div>
          <div v-if="n.provider_message_id" class="col-span-2"><dt class="text-muted-foreground">{{ $t('occurrences.noticeProviderId') }}</dt><dd class="font-mono break-all">{{ n.provider_message_id }}</dd></div>
          <div v-if="n.last_error" class="col-span-2"><dt class="text-muted-foreground">{{ $t('occurrences.noticeError') }}</dt><dd class="text-destructive">{{ n.last_error }}</dd></div>
        </dl>
        <details v-if="n.attempt_history && n.attempt_history.length">
          <summary class="text-xs text-muted-foreground cursor-pointer">{{ $t('occurrences.noticeAttempts', { count: n.attempt_history.length }) }}</summary>
          <ul class="mt-1 space-y-1 text-xs">
            <li v-for="a in n.attempt_history" :key="a.number" class="flex flex-wrap gap-2">
              <span class="font-medium">#{{ a.number }}</span>
              <span>{{ formatDateTime(a.started_at) }}</span>
              <span>{{ statusLabel(a.outcome) }}</span>
              <span v-if="a.error" class="text-destructive">{{ a.error }}</span>
            </li>
          </ul>
        </details>
        <div v-if="n.can_retry || n.can_cancel" class="flex gap-2">
          <Button v-if="n.can_retry" size="sm" variant="outline" :disabled="isBusy" data-testid="notification-retry" @click="act(n, 'retry')">
            {{ n.status === 'uncertain' ? $t('occurrences.noticeRetryRisk') : $t('occurrences.noticeRetry') }}
          </Button>
          <Button v-if="n.can_cancel" size="sm" variant="ghost" :disabled="isBusy" data-testid="notification-cancel" @click="act(n, 'cancel')">
            {{ $t('occurrences.noticeCancel') }}
          </Button>
        </div>
      </div>
    </CardContent>
  </Card>
</template>
