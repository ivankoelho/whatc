<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Cloud, Loader2 } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { CalendarClock } from 'lucide-vue-next'
import { nextcloudService, type NextcloudStatus, type NextcloudCalendarSettings } from '@/services/api'
import { chatErrorCode } from '@/lib/team-chat'

// The organization's Nextcloud server, registered once here. Each person then connects with their
// own app password from the paperclip menu of the Chat Interno (nobody's password is kept here).
const { t } = useI18n()
const status = ref<NextcloudStatus | null>(null)
const loading = ref(true)
const saving = ref(false)
const baseUrl = ref('')
const unwrap = <T,>(res: { data: unknown }): T => ((res.data as { data?: unknown }).data ?? res.data) as T

async function load() {
  loading.value = true
  try {
    status.value = unwrap<NextcloudStatus>(await nextcloudService.status())
    baseUrl.value = status.value.base_url ?? ''
  } catch {
    toast.error(t('teamChat.errors.generic'))
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    await nextcloudService.saveServer(baseUrl.value.trim())
    toast.success(t('integrations.nextcloud.saved'))
    await load()
  } catch (err) {
    const c = chatErrorCode(err)
    toast.error(t(c === 'nextcloud_bad_url' ? 'teamChat.nextcloud.errors.nextcloud_bad_url' : 'teamChat.errors.generic'))
  } finally {
    saving.value = false
  }
}

// --- the calendar the AI agent books meetings in ---------------------------------------------
// A service account of the organization (a dedicated Nextcloud user) with its own app password, and the
// booking rules. The password is never shown again: leaving it empty keeps the one stored.
const WEEKDAYS = [1, 2, 3, 4, 5, 6, 0] // Monday first
const cal = ref({
  enabled: false, calendar_path: '', username: '', app_password: '', time_zone: 'America/Sao_Paulo',
  slot_minutes: 30, buffer_minutes: 0, min_notice_hours: 2, lookahead_days: 14, location: '',
})
const days = ref<Record<number, { on: boolean; from: string; to: string }>>(
  Object.fromEntries([0, 1, 2, 3, 4, 5, 6].map(d => [d, { on: d >= 1 && d <= 5, from: '09:00', to: '18:00' }])),
)
const calPasswordSet = ref(false)
const calSaving = ref(false)

function hoursPayload(): Record<string, string> {
  const out: Record<string, string> = {}
  for (const d of WEEKDAYS) if (days.value[d].on) out[String(d)] = `${days.value[d].from}-${days.value[d].to}`
  return out
}

async function loadCalendar() {
  try {
    const res = unwrap<{ configured: boolean; calendar?: NextcloudCalendarSettings; password_set?: boolean }>(await nextcloudService.getCalendar())
    if (!res.configured || !res.calendar) return
    const c = res.calendar
    cal.value = { ...cal.value, enabled: c.enabled, calendar_path: c.calendar_path, username: c.username, time_zone: c.time_zone,
      slot_minutes: c.slot_minutes, buffer_minutes: c.buffer_minutes, min_notice_hours: c.min_notice_hours, lookahead_days: c.lookahead_days, location: c.location ?? '' }
    calPasswordSet.value = !!res.password_set
    for (const d of [0, 1, 2, 3, 4, 5, 6]) {
      const h = c.hours?.[String(d)]
      days.value[d] = h ? { on: true, from: h.split('-')[0], to: h.split('-')[1] } : { ...days.value[d], on: false }
    }
  } catch {
    // the calendar is optional: the card simply starts empty
  }
}

async function saveCalendar() {
  calSaving.value = true
  try {
    const { app_password, ...rest } = cal.value
    await nextcloudService.saveCalendar({ ...rest, hours: hoursPayload(), ...(app_password ? { app_password } : {}) })
    toast.success(t('integrations.nextcloud.calendar.saved'))
    cal.value.app_password = ''
    await loadCalendar()
  } catch (err) {
    const c = chatErrorCode(err)
    const key = `integrations.nextcloud.calendar.errors.${c}`
    toast.error(c && t(key) !== key ? t(key) : t('teamChat.errors.generic'))
  } finally {
    calSaving.value = false
  }
}

onMounted(() => { load(); loadCalendar() })
</script>

<template>
  <!-- The settings hub gives a tab no scroll of its own: this card list is long (the calendar), so it scrolls here -->
  <div class="h-full overflow-y-auto" data-testid="nextcloud-settings-scroll">
  <div class="mx-auto max-w-2xl space-y-4 p-6">
    <Card>
      <CardHeader>
        <CardTitle class="flex items-center gap-2 text-base"><Cloud class="h-4 w-4" aria-hidden="true" />{{ t('integrations.nextcloud.title') }}</CardTitle>
        <CardDescription>{{ t('integrations.nextcloud.description') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <div v-if="loading" class="flex justify-center py-4"><Loader2 class="h-5 w-5 animate-spin text-muted-foreground" /></div>
        <form v-else class="space-y-3" @submit.prevent="save">
          <div class="space-y-1.5">
            <Label for="nc-settings-url">{{ t('teamChat.nextcloud.serverUrl') }}</Label>
            <Input id="nc-settings-url" v-model="baseUrl" placeholder="https://nextcloud.suaempresa.com.br" data-testid="nextcloud-settings-url" />
            <p class="text-xs text-muted-foreground">{{ status?.configured ? t('integrations.nextcloud.changeWarning') : t('integrations.nextcloud.notConfigured') }}</p>
          </div>
          <div class="flex justify-end">
            <Button type="submit" :disabled="saving || !baseUrl.trim()" data-testid="nextcloud-settings-save">
              <Loader2 v-if="saving" class="mr-2 h-4 w-4 animate-spin" />{{ t('common.save') }}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>

    <Card v-if="status?.configured" data-testid="calendar-card">
      <CardHeader>
        <CardTitle class="flex items-center gap-2 text-base"><CalendarClock class="h-4 w-4" aria-hidden="true" />{{ t('integrations.nextcloud.calendar.title') }}</CardTitle>
        <CardDescription>{{ t('integrations.nextcloud.calendar.description') }}</CardDescription>
      </CardHeader>
      <CardContent>
        <form class="space-y-3" data-testid="calendar-form" @submit.prevent="saveCalendar">
          <label class="flex items-center gap-2 text-sm">
            <Checkbox :checked="cal.enabled" data-testid="calendar-enabled" @update:checked="(v: boolean | 'indeterminate') => (cal.enabled = v === true)" />
            {{ t('integrations.nextcloud.calendar.enabled') }}
          </label>
          <div class="space-y-1.5">
            <Label for="cal-path">{{ t('integrations.nextcloud.calendar.path') }}</Label>
            <Input id="cal-path" v-model="cal.calendar_path" placeholder="/remote.php/dav/calendars/triagem/personal/" data-testid="calendar-path" />
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div class="space-y-1.5">
              <Label for="cal-user">{{ t('integrations.nextcloud.calendar.username') }}</Label>
              <Input id="cal-user" v-model="cal.username" autocomplete="off" data-testid="calendar-username" />
            </div>
            <div class="space-y-1.5">
              <Label for="cal-pass">{{ t('integrations.nextcloud.calendar.password') }}</Label>
              <Input id="cal-pass" v-model="cal.app_password" type="password" autocomplete="new-password"
                :placeholder="calPasswordSet ? t('integrations.nextcloud.calendar.passwordKept') : ''" data-testid="calendar-password" />
            </div>
          </div>
          <div class="grid grid-cols-2 gap-3">
            <div class="space-y-1.5">
              <Label for="cal-slot">{{ t('integrations.nextcloud.calendar.slot') }}</Label>
              <Input id="cal-slot" v-model.number="cal.slot_minutes" type="number" min="15" max="240" step="5" data-testid="calendar-slot" />
            </div>
            <div class="space-y-1.5">
              <Label for="cal-buffer">{{ t('integrations.nextcloud.calendar.buffer') }}</Label>
              <Input id="cal-buffer" v-model.number="cal.buffer_minutes" type="number" min="0" max="120" step="5" />
            </div>
            <div class="space-y-1.5">
              <Label for="cal-notice">{{ t('integrations.nextcloud.calendar.notice') }}</Label>
              <Input id="cal-notice" v-model.number="cal.min_notice_hours" type="number" min="0" max="168" />
            </div>
            <div class="space-y-1.5">
              <Label for="cal-look">{{ t('integrations.nextcloud.calendar.lookahead') }}</Label>
              <Input id="cal-look" v-model.number="cal.lookahead_days" type="number" min="1" max="60" />
            </div>
          </div>
          <div class="space-y-1.5">
            <Label for="cal-loc">{{ t('integrations.nextcloud.calendar.location') }}</Label>
            <Input id="cal-loc" v-model="cal.location" maxlength="200" />
          </div>
          <fieldset class="space-y-1.5">
            <legend class="text-sm font-medium">{{ t('integrations.nextcloud.calendar.hours') }}</legend>
            <div v-for="d in WEEKDAYS" :key="d" class="flex items-center gap-2 text-sm">
              <label class="flex w-28 items-center gap-2">
                <Checkbox :checked="days[d].on" :data-testid="`calendar-day-${d}`" @update:checked="(v: boolean | 'indeterminate') => (days[d].on = v === true)" />
                {{ t(`integrations.nextcloud.calendar.weekdays.${d}`) }}
              </label>
              <Input v-model="days[d].from" type="time" class="w-28" :disabled="!days[d].on" :aria-label="t('integrations.nextcloud.calendar.from')" />
              <span aria-hidden="true">–</span>
              <Input v-model="days[d].to" type="time" class="w-28" :disabled="!days[d].on" :aria-label="t('integrations.nextcloud.calendar.to')" />
            </div>
          </fieldset>
          <div class="flex justify-end">
            <Button type="submit" :disabled="calSaving" data-testid="calendar-save">
              <Loader2 v-if="calSaving" class="mr-2 h-4 w-4 animate-spin" />{{ t('common.save') }}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  </div>
  </div>
</template>
