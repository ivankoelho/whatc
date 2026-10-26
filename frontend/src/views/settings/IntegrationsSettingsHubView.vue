<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import { Key, Webhook, Zap, Plug, Brain, Cloud } from 'lucide-vue-next'
import AISettingsView from './AISettingsView.vue'
import APIKeysView from './APIKeysView.vue'
import WebhooksView from './WebhooksView.vue'
import CustomActionsView from './CustomActionsView.vue'
import XProcessIntegrationView from './XProcessIntegrationView.vue'
import NextcloudSettingsView from './NextcloudSettingsView.vue'

// IA first: the AI provider is the integration people look for most.
const tabs: HubTab[] = [
  { value: 'ai', icon: Brain, labelKey: 'chatbotSettings.ai', permission: 'settings.chatbot', component: AISettingsView },
  { value: 'api-keys', icon: Key, labelKey: 'nav.apiKeys', permission: 'api_keys', component: APIKeysView },
  { value: 'webhooks', icon: Webhook, labelKey: 'nav.webhooks', permission: 'webhooks', component: WebhooksView },
  { value: 'custom-actions', icon: Zap, labelKey: 'nav.customActions', permission: 'custom_actions', component: CustomActionsView },
  { value: 'nextcloud', icon: Cloud, labelKey: 'integrations.nextcloud.tab', permission: 'settings.general', component: NextcloudSettingsView },
  { value: 'xprocess', icon: Plug, labelKey: 'nav.xprocessIntegration', permission: 'xprocess_integration', component: XProcessIntegrationView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="ai" />
</template>
