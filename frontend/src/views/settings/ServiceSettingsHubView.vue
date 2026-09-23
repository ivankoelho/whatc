<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import ChatbotSettingsView from './ChatbotSettingsView.vue'
import CannedResponsesView from './CannedResponsesView.vue'
import TagsView from './TagsView.vue'
import ContactsView from './ContactsView.vue'

const tabs: HubTab[] = [
  { value: 'chatbot', labelKey: 'nav.chatbot', permission: 'settings.chatbot', component: ChatbotSettingsView },
  { value: 'canned-responses', labelKey: 'nav.cannedResponses', permission: 'canned_responses', component: CannedResponsesView },
  { value: 'tags', labelKey: 'nav.tags', permission: 'tags', component: TagsView },
  { value: 'contacts', labelKey: 'nav.contacts', permission: 'contacts', component: ContactsView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="chatbot" />
</template>
