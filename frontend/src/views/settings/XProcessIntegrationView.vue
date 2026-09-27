<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { xprocessIntegrationService } from '@/services/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

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
    toast.success(t('common.saved'))
  } catch {
    toast.error(t('xprocessIntegration.saveFailure'))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="max-w-lg space-y-4">
    <div>
      <h3 class="text-lg font-medium">{{ t('xprocessIntegration.title') }}</h3>
      <p class="text-sm text-muted-foreground">{{ t('xprocessIntegration.description') }}</p>
    </div>

    <div class="space-y-2">
      <Label for="xprocess-base-url">{{ t('xprocessIntegration.baseUrl') }}</Label>
      <Input id="xprocess-base-url" v-model="baseUrl" />
    </div>

    <div class="space-y-2">
      <Label for="xprocess-api-key">{{ t('xprocessIntegration.apiKey') }}</Label>
      <Input id="xprocess-api-key" v-model="apiKey" type="password"
        :placeholder="isConfigured ? t('xprocessIntegration.apiKeyConfigured') : ''" />
    </div>

    <div class="flex gap-2">
      <Button variant="outline" :disabled="testing" @click="testConnection">
        {{ t('xprocessIntegration.testConnection') }}
      </Button>
      <Button :disabled="saving || !apiKey" @click="save">
        {{ t('xprocessIntegration.save') }}
      </Button>
    </div>
  </div>
</template>
