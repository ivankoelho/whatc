<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import { Users, User, ShieldCheck, Building2, Network } from 'lucide-vue-next'
import TeamsView from './TeamsView.vue'
import UsersView from './UsersView.vue'
import RolesView from './RolesView.vue'
import UnitsView from './UnitsView.vue'
import DepartmentsView from './DepartmentsView.vue'

const tabs: HubTab[] = [
  { value: 'teams', icon: Users, labelKey: 'nav.teams', permission: 'teams', component: TeamsView },
  { value: 'users', icon: User, labelKey: 'nav.users', permission: 'users', component: UsersView },
  { value: 'roles', icon: ShieldCheck, labelKey: 'nav.roles', permission: 'roles', component: RolesView },
  // The company structure people and teams hang on (moved here from General).
  { value: 'units', icon: Building2, labelKey: 'nav.units', permission: 'units', alsoNeeds: 'occurrences', component: UnitsView },
  { value: 'departments', icon: Network, labelKey: 'nav.departments', permission: 'departments', alsoNeeds: 'occurrences', component: DepartmentsView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="teams" />
</template>
