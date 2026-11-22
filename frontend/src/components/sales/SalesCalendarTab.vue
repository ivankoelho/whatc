<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { CalendarDays, ExternalLink, Loader2 } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { nextcloudService } from '@/services/api'

// The organization's Nextcloud calendar, opened in its own browser tab (it is not framed inside the CRM). The address is saved by an administrator (Integrações,
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

    <!-- The calendar opens in its own tab: Nextcloud's page has its own layout and does not belong inside ours -->
    <div v-else class="flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center" data-testid="crm-calendar-launcher">
      <CalendarDays class="h-10 w-10 text-muted-foreground" aria-hidden="true" />
      <p class="max-w-md text-sm text-muted-foreground">{{ t('sales.calendarExternalNote') }}</p>
      <Button as-child size="sm">
        <a :href="url" target="_blank" rel="noopener noreferrer" data-testid="crm-calendar-open">
          <ExternalLink class="mr-1.5 h-4 w-4" aria-hidden="true" />{{ t('sales.calendarOpenCta') }}
        </a>
      </Button>
    </div>
  </div>
</template>
