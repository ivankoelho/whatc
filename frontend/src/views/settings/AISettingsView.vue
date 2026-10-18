<script setup lang="ts">
// AI provider settings (Settings → Integrations → IA). Moved here from the
// chatbot settings so every external integration sits in one place; same
// settings document as before (GET/PUT /chatbot/settings, ai_* fields), so
// the chatbot keeps reading them unchanged.
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { PageHeader, AuditLogPanel } from '@/components/shared'
import { toast } from 'vue-sonner'
import { getErrorMessage } from '@/lib/api-utils'
import { knowledgeRagSwitch } from '@/lib/knowledge'
import { Brain, Loader2 } from 'lucide-vue-next'
import { chatbotService } from '@/services/api'
import { useAuthStore } from '@/stores/auth'

const { t } = useI18n()
const authStore = useAuthStore()

// Same org resolution as the chatbot settings (super admins may switch orgs).
const orgID = computed(
  () => localStorage.getItem('selected_organization_id') || authStore.organizationId,
)
const aiLogKey = ref(0)
function refreshActivityLog(key: typeof aiLogKey) {
  setTimeout(() => { key.value++ }, 500)
}

const isSubmitting = ref(false)

// AI Settings
const aiSettings = ref({
  ai_enabled: false,
  ai_provider: '',
  ai_api_key: '',
  // Whether a key is stored. The key itself is never sent to the browser.
  ai_api_key_configured: false,
  ai_model: '',
  ai_max_tokens: 500,
  ai_system_prompt: '',
  // Knowledge in the AI replies: the organization's own switch, and whether the SERVER allows it (read-only)
  knowledge_enabled: false,
  knowledge_rag_available: false
})

const isAIEnabled = ref(false)
const knowledgeSwitch = computed(() => knowledgeRagSwitch(aiSettings.value.knowledge_rag_available))

// Static suggestions only for the providers that had them before; Groq has none
// on purpose: its models always come from the provider's own list.
const aiProviders = [
  { value: 'openai', label: 'OpenAI', models: ['gpt-4o', 'gpt-4o-mini', 'gpt-4-turbo', 'gpt-3.5-turbo'] },
  { value: 'anthropic', label: 'Anthropic', models: ['claude-3-5-sonnet-latest', 'claude-3-5-haiku-latest', 'claude-3-opus-latest'] },
  { value: 'google', label: 'Google AI', models: ['gemini-2.0-flash', 'gemini-2.0-flash-lite', 'gemini-1.5-flash', 'gemini-1.5-flash-8b'] },
  { value: 'groq', label: 'Groq', models: [] as string[] }
]

// Models loaded from the provider (POST /chatbot/ai/models), per provider.
const loadedModels = ref<Record<string, string[]>>({})
const isLoadingModels = ref(false)

const availableModels = computed(() => {
  const provider = aiProviders.find(p => p.value === aiSettings.value.ai_provider)
  const list = loadedModels.value[aiSettings.value.ai_provider] ?? provider?.models ?? []
  // Keep the saved model selectable even if the provider no longer lists it.
  const current = aiSettings.value.ai_model
  return current && !list.includes(current) ? [current, ...list] : list
})

const canLoadModels = computed(() =>
  !!aiSettings.value.ai_provider &&
  (!!aiSettings.value.ai_api_key || aiSettings.value.ai_api_key_configured)
)

async function loadAIModels() {
  const provider = aiSettings.value.ai_provider
  if (!provider) return
  isLoadingModels.value = true
  try {
    const response = await chatbotService.listAIModels(provider, aiSettings.value.ai_api_key || undefined)
    const data = response.data.data || response.data
    loadedModels.value = { ...loadedModels.value, [provider]: (data.models || []).map((m: { id: string }) => m.id) }
    toast.success(t('chatbotSettings.modelsLoaded', { count: loadedModels.value[provider].length }))
  } catch (error) {
    toast.error(getErrorMessage(error, t('chatbotSettings.failedLoadModels')))
  } finally {
    isLoadingModels.value = false
  }
}

// A key (typed or saved) belongs to one provider: switching provider drops the
// typed key and the previous provider's model so they are never mixed.
watch(() => aiSettings.value.ai_provider, (next, prev) => {
  if (prev !== undefined && prev !== '' && next !== prev) {
    aiSettings.value.ai_api_key = ''
    aiSettings.value.ai_model = ''
  }
})

watch(isAIEnabled, (newValue) => {
  aiSettings.value.ai_enabled = newValue
})

onMounted(async () => {
  try {
    const response = await chatbotService.getSettings()
    const settings = (response.data.data || response.data).settings
    if (!settings) return
    const aiEnabledValue = settings.ai_enabled === true
    isAIEnabled.value = aiEnabledValue
    aiSettings.value = {
      ai_enabled: aiEnabledValue,
      ai_provider: settings.ai_provider || '',
      ai_api_key: '',
      ai_api_key_configured: settings.ai_api_key_configured === true,
      ai_model: settings.ai_model || '',
      ai_max_tokens: settings.ai_max_tokens || 500,
      ai_system_prompt: settings.ai_system_prompt || '',
      knowledge_enabled: settings.knowledge_enabled === true,
      knowledge_rag_available: settings.knowledge_rag_available === true
    }
  } catch (error) {
    toast.error(getErrorMessage(error, t('chatbotSettings.aiSaveFailed')))
  }
})

async function saveAISettings() {
  isSubmitting.value = true
  try {
    const payload: any = {
      ai_enabled: aiSettings.value.ai_enabled,
      ai_provider: aiSettings.value.ai_provider,
      ai_model: aiSettings.value.ai_model,
      ai_max_tokens: aiSettings.value.ai_max_tokens,
      ai_system_prompt: aiSettings.value.ai_system_prompt,
      knowledge_enabled: aiSettings.value.knowledge_enabled
    }
    if (aiSettings.value.ai_api_key) {
      payload.ai_api_key = aiSettings.value.ai_api_key
    }
    await chatbotService.updateSettings(payload)
    toast.success(t('chatbotSettings.aiSettingsSaved'))
    if (aiSettings.value.ai_api_key) aiSettings.value.ai_api_key_configured = true
    aiSettings.value.ai_api_key = ''
    refreshActivityLog(aiLogKey)
  } catch (error: any) {
    toast.error(error?.response?.data?.error_type === 'AIEncryptionKeyUnavailable'
      ? t('chatbotSettings.aiEncryptionKeyMissing')
      : t('chatbotSettings.aiSaveFailed'))
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('chatbotSettings.aiSettings')" :subtitle="$t('chatbotSettings.aiSettingsDesc')" :icon="Brain" icon-gradient="bg-gradient-to-br from-emerald-500 to-teal-600 shadow-emerald-500/20" />
    <ScrollArea class="flex-1">
      <div class="p-6 space-y-4 max-w-4xl mx-auto">
        <Card>
          <CardHeader>
            <CardTitle>{{ $t('chatbotSettings.aiSettings') }}</CardTitle>
            <CardDescription>{{ $t('chatbotSettings.aiSettingsDesc') }}</CardDescription>
          </CardHeader>
          <CardContent class="space-y-4">
            <div class="flex items-center justify-between">
              <div>
                <p class="font-medium">{{ $t('chatbotSettings.enableAiResponses') }}</p>
                <p class="text-sm text-muted-foreground">{{ $t('chatbotSettings.enableAiResponsesDesc') }}</p>
              </div>
              <Switch
                :checked="isAIEnabled"
                @update:checked="(val: boolean) => isAIEnabled = val"
              />
            </div>

            <div v-if="isAIEnabled" class="space-y-4 pt-2">
              <Separator />

              <div class="grid grid-cols-2 gap-4">
                <div class="space-y-2">
                  <Label>{{ $t('chatbotSettings.aiProvider') }}</Label>
                  <Select v-model="aiSettings.ai_provider">
                    <SelectTrigger>
                      <SelectValue :placeholder="$t('chatbotSettings.selectProvider') + '...'" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem v-for="provider in aiProviders" :key="provider.value" :value="provider.value">
                        {{ provider.label }}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div class="space-y-2">
                  <div class="flex items-center justify-between">
                    <Label>{{ $t('chatbotSettings.model') }}</Label>
                    <Button
                      variant="link"
                      size="sm"
                      class="h-auto p-0 text-xs"
                      :disabled="!canLoadModels || isLoadingModels"
                      data-testid="load-ai-models"
                      @click="loadAIModels"
                    >
                      <Loader2 v-if="isLoadingModels" class="mr-1 h-3 w-3 animate-spin" />
                      {{ $t('chatbotSettings.loadModels') }}
                    </Button>
                  </div>
                  <Select v-model="aiSettings.ai_model" :disabled="!aiSettings.ai_provider">
                    <SelectTrigger>
                      <SelectValue :placeholder="$t('chatbotSettings.selectModel') + '...'" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem v-for="model in availableModels" :key="model" :value="model">
                        {{ model }}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>

              <div class="space-y-2">
                <Label>{{ $t('chatbotSettings.apiKey') }}</Label>
                <Input
                  v-model="aiSettings.ai_api_key"
                  type="password"
                  :placeholder="$t('chatbotSettings.apiKeyPlaceholder') + '...'"
                />
                <p class="text-xs text-muted-foreground">{{ $t('chatbotSettings.apiKeyHint') }}</p>
                <p v-if="aiSettings.ai_api_key_configured && !aiSettings.ai_api_key" class="text-xs text-emerald-600" data-testid="ai-key-saved">
                  {{ $t('chatbotSettings.apiKeySaved') }}
                </p>
                <p v-if="aiSettings.ai_provider && !availableModels.length" class="text-xs text-muted-foreground">
                  {{ $t('chatbotSettings.loadModelsHint') }}
                </p>
              </div>

              <div class="space-y-2">
                <Label>{{ $t('chatbotSettings.maxTokens') }}</Label>
                <Input v-model.number="aiSettings.ai_max_tokens" type="number" min="100" max="4000" class="w-32" />
              </div>

              <div class="space-y-2">
                <Label>{{ $t('chatbotSettings.systemPrompt') }}</Label>
                <Textarea
                  v-model="aiSettings.ai_system_prompt"
                  :placeholder="$t('chatbotSettings.systemPromptPlaceholder') + '...'"
                  :rows="3"
                />
              </div>

              <div class="flex items-center justify-between gap-4 py-2" data-testid="chatbot-knowledge-switch">
                <div>
                  <p class="font-medium">{{ $t('chatbotSettings.knowledgeTitle') }}</p>
                  <p class="text-sm text-muted-foreground">{{ $t('chatbotSettings.knowledgeDesc') }}</p>
                  <p class="text-xs text-muted-foreground mt-1">{{ $t(knowledgeSwitch.hint === 'ready' ? 'chatbotSettings.knowledgeReady' : 'chatbotSettings.knowledgeServerOff') }}</p>
                </div>
                <Switch
                  :checked="aiSettings.knowledge_enabled"
                  :disabled="knowledgeSwitch.disabled"
                  @update:checked="aiSettings.knowledge_enabled = $event"
                />
              </div>
            </div>

            <div class="flex justify-end pt-2">
              <Button @click="saveAISettings" :disabled="isSubmitting">
                <Loader2 v-if="isSubmitting" class="mr-2 h-4 w-4 animate-spin" />
                {{ $t('chatbotSettings.saveChanges') }}
              </Button>
            </div>
          </CardContent>
        </Card>
        <div v-if="orgID" class="mt-4">
          <AuditLogPanel :key="aiLogKey" resource-type="settings.chatbot.ai" :resource-id="orgID" />
        </div>
      </div>
    </ScrollArea>
  </div>
</template>
