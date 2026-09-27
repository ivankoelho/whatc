<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import APIKeysView from './APIKeysView.vue'
import WebhooksView from './WebhooksView.vue'
import CustomActionsView from './CustomActionsView.vue'
import XProcessIntegrationView from './XProcessIntegrationView.vue'

const tabs: HubTab[] = [
  { value: 'api-keys', labelKey: 'nav.apiKeys', permission: 'api_keys', component: APIKeysView },
  { value: 'webhooks', labelKey: 'nav.webhooks', permission: 'webhooks', component: WebhooksView },
  { value: 'custom-actions', labelKey: 'nav.customActions', permission: 'custom_actions', component: CustomActionsView },
  { value: 'xprocess', labelKey: 'nav.xprocessIntegration', permission: 'xprocess_integration', component: XProcessIntegrationView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="api-keys" />
</template>
