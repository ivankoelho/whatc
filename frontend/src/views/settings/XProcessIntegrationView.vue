<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { xprocessIntegrationService } from '@/services/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { PageHeader } from '@/components/shared'
import { Plug, Loader2 } from 'lucide-vue-next'

const { t } = useI18n()

const baseUrl = ref('https://api.atacadaodospisos.com.br')
const apiKey = ref('')
const isConfigured = ref(false)
const testing = ref(false)
const saving = ref(false)

async function load() {
  const res = await xprocessIntegrationService.get()
  if (res.data.data.base_url) baseUrl.value = res.data.data.base_url
  isConfigured.value = res.data.data.is_configured
}

async function testConnection() {
  if (!apiKey.value) {
    toast.error(t('xprocessIntegration.testFailure'))
    return
  }
  testing.value = true
  try {
    await xprocessIntegrationService.test({ base_url: baseUrl.value, api_key: apiKey.value })
    toast.success(t('xprocessIntegration.testSuccess'))
  } catch {
    toast.error(t('xprocessIntegration.testFailure'))
  } finally {
    testing.value = false
  }
}

async function save() {
  if (!apiKey.value) return
  saving.value = true
  try {
    await xprocessIntegrationService.upsert({ base_url: baseUrl.value, api_key: apiKey.value })
    apiKey.value = ''
    isConfigured.value = true
    toast.success(t('xprocessIntegration.saveSuccess'))
  } catch {
    toast.error(t('xprocessIntegration.saveFailure'))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="flex flex-col h-full">
    <PageHeader
      :title="t('xprocessIntegration.title')"
      :description="t('xprocessIntegration.description')"
      :icon="Plug"
      icon-gradient="bg-gradient-to-br from-teal-500 to-cyan-600 shadow-teal-500/20"
      back-link="/settings/integrations"
    />

    <ScrollArea class="flex-1">
      <div class="p-6">
        <Card class="max-w-lg">
          <CardHeader>
            <CardTitle>{{ t('xprocessIntegration.credentialTitle') }}</CardTitle>
            <CardDescription>{{ t('xprocessIntegration.credentialDescription') }}</CardDescription>
          </CardHeader>
          <CardContent class="space-y-4">
            <div class="space-y-2">
              <Label for="xprocess-base-url">{{ t('xprocessIntegration.baseUrl') }}</Label>
              <Input id="xprocess-base-url" v-model="baseUrl" />
            </div>

            <div class="space-y-2">
              <Label for="xprocess-api-key">{{ t('xprocessIntegration.apiKey') }}</Label>
              <Input
                id="xprocess-api-key"
                v-model="apiKey"
                type="password"
                :placeholder="isConfigured ? t('xprocessIntegration.apiKeyConfigured') : ''"
              />
            </div>

            <div class="flex gap-2 pt-2">
              <Button variant="outline" :disabled="testing" @click="testConnection">
                <Loader2 v-if="testing" class="h-4 w-4 mr-2 animate-spin" />
                {{ t('xprocessIntegration.testConnection') }}
              </Button>
              <Button :disabled="saving || !apiKey" @click="save">
                <Loader2 v-if="saving" class="h-4 w-4 mr-2 animate-spin" />
                {{ t('xprocessIntegration.save') }}
              </Button>
            </div>
          </CardContent>
        </Card>
      </div>
    </ScrollArea>
  </div>
</template>
