<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import OccurrenceStagesView from './OccurrenceStagesView.vue'
import OccurrenceCategoriesView from './OccurrenceCategoriesView.vue'
import OccurrenceWhatHappenedView from './OccurrenceWhatHappenedView.vue'
import OccurrenceProcessesView from './OccurrenceProcessesView.vue'
import OccurrenceSLAPoliciesView from './OccurrenceSLAPoliciesView.vue'
import UnitsView from './UnitsView.vue'

const tabs: HubTab[] = [
  { value: 'stages', labelKey: 'nav.occurrenceStages', permission: 'occurrences.stages', component: OccurrenceStagesView },
  { value: 'categories', labelKey: 'nav.occurrenceCategories', permission: 'occurrences.categories', component: OccurrenceCategoriesView },
  { value: 'what-happened', labelKey: 'nav.whatHappened', permission: 'occurrences.what_happened', component: OccurrenceWhatHappenedView },
  { value: 'processes', labelKey: 'nav.occurrenceProcesses', permission: 'occurrences.processes', component: OccurrenceProcessesView },
  { value: 'sla', labelKey: 'nav.slaPolicies', permission: 'occurrences.sla_policies', component: OccurrenceSLAPoliciesView },
  { value: 'units', labelKey: 'nav.units', permission: 'units', component: UnitsView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="stages" />
</template>
