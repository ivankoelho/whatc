<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import WhatsAppUsageConsumptionView from './WhatsAppUsageConsumptionView.vue'
import WhatsAppRatesView from './WhatsAppRatesView.vue'

// Both tabs need whatsapp_usage:read; the price table's edit buttons need :write on top.
const tabs: HubTab[] = [
  { value: 'consumption', labelKey: 'whatsappUsage.tabConsumption', permission: 'whatsapp_usage', component: WhatsAppUsageConsumptionView },
  { value: 'rates', labelKey: 'whatsappUsage.tabRates', permission: 'whatsapp_usage', component: WhatsAppRatesView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="consumption" />
</template>
