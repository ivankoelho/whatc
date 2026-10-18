<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import { BarChart3, Receipt } from 'lucide-vue-next'
import WhatsAppUsageConsumptionView from './WhatsAppUsageConsumptionView.vue'
import WhatsAppRatesView from './WhatsAppRatesView.vue'

// Consumption needs whatsapp_usage:read; the price table is super-admin only (also enforced by the API).
const tabs: HubTab[] = [
  { value: 'consumption', icon: BarChart3, labelKey: 'whatsappUsage.tabConsumption', permission: 'whatsapp_usage', component: WhatsAppUsageConsumptionView },
  { value: 'rates', icon: Receipt, superAdminOnly: true, labelKey: 'whatsappUsage.tabRates', permission: 'whatsapp_usage', component: WhatsAppRatesView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="consumption" />
</template>
