<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import { Columns3, FolderTree, MessageSquareWarning, Workflow, Timer } from 'lucide-vue-next'
import OccurrenceStagesView from './OccurrenceStagesView.vue'
import OccurrenceCategoriesView from './OccurrenceCategoriesView.vue'
import OccurrenceWhatHappenedView from './OccurrenceWhatHappenedView.vue'
import OccurrenceProcessesView from './OccurrenceProcessesView.vue'
import OccurrenceSLAPoliciesView from './OccurrenceSLAPoliciesView.vue'

const tabs: HubTab[] = [
  { value: 'stages', icon: Columns3, labelKey: 'nav.occurrenceStages', permission: 'occurrences.stages', component: OccurrenceStagesView },
  { value: 'categories', icon: FolderTree, labelKey: 'nav.occurrenceCategories', permission: 'occurrences.categories', component: OccurrenceCategoriesView },
  { value: 'what-happened', icon: MessageSquareWarning, labelKey: 'nav.whatHappened', permission: 'occurrences.what_happened', component: OccurrenceWhatHappenedView },
  { value: 'processes', icon: Workflow, labelKey: 'nav.occurrenceProcesses', permission: 'occurrences.processes', component: OccurrenceProcessesView },
  { value: 'sla', icon: Timer, labelKey: 'nav.slaPolicies', permission: 'occurrences.sla_policies', component: OccurrenceSLAPoliciesView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="stages" />
</template>
