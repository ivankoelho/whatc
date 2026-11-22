<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { CalendarDays, ExternalLink, Loader2 } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { nextcloudService } from '@/services/api'

// The organization's Nextcloud calendar, framed inside the CRM. The address is saved by an administrator (Integrações,
// Nextcloud) and checked by the server: it is always the calendar app's embed page on the organization's own Nextcloud.
const { t } = useI18n()
const loading = ref(true)
const failed = ref(false)
const url = ref('')
const canConfigure = ref(false)

onMounted(async () => {
  try {
    const res = await nextcloudService.getCalendarEmbed()
    const data = (res.data as { data?: { url: string; can_configure: boolean } }).data ?? (res.data as { url: string; can_configure: boolean })
    url.value = data.url ?? ''
    canConfigure.value = !!data.can_configure
  } catch {
    failed.value = true
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="flex h-full min-h-0 flex-col" data-testid="crm-calendar-tab">
    <div v-if="loading" class="flex flex-1 items-center justify-center"><Loader2 class="h-5 w-5 animate-spin text-muted-foreground" aria-hidden="true" /></div>

    <div v-else-if="failed" class="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center" data-testid="crm-calendar-failed">
      <p class="text-sm text-muted-foreground">{{ t('sales.calendarLoadFailed') }}</p>
    </div>

    <div v-else-if="!url" class="flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center" data-testid="crm-calendar-empty">
      <CalendarDays class="h-10 w-10 text-muted-foreground" aria-hidden="true" />
      <p class="max-w-md text-sm text-muted-foreground">{{ canConfigure ? t('sales.calendarEmptyAdmin') : t('sales.calendarEmpty') }}</p>
      <Button v-if="canConfigure" as-child size="sm" variant="outline">
        <RouterLink to="/settings/integrations?tab=nextcloud">{{ t('sales.calendarConfigure') }}</RouterLink>
      </Button>
    </div>

    <template v-else>
      <div class="flex shrink-0 justify-end px-4 pt-3">
        <a
          :href="url"
          target="_blank"
          rel="noopener noreferrer"
          class="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
          data-testid="crm-calendar-open"
        ><ExternalLink class="h-3.5 w-3.5" aria-hidden="true" />{{ t('sales.calendarOpen') }}</a>
      </div>
      <!-- The framed page is another origin; the sandbox lets the calendar run and open its links, nothing more -->
      <iframe
        :src="url"
        :title="t('sales.tabCalendar')"
        class="m-4 mt-2 min-h-0 flex-1 rounded-xl border-0 bg-white shadow-sm"
        loading="lazy"
        referrerpolicy="no-referrer"
        sandbox="allow-scripts allow-same-origin allow-popups allow-forms"
        data-testid="crm-calendar-frame"
      />
    </template>
  </div>
</template>
