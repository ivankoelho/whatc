<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Cloud, Loader2 } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { nextcloudService, type NextcloudStatus } from '@/services/api'
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

onMounted(load)
</script>

<template>
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
  </div>
</template>
