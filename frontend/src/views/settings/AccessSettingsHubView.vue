<script setup lang="ts">
import { SettingsTabHub, type HubTab } from '@/components/shared'
import TeamsView from './TeamsView.vue'
import UsersView from './UsersView.vue'
import RolesView from './RolesView.vue'

const tabs: HubTab[] = [
  { value: 'teams', labelKey: 'nav.teams', permission: 'teams', component: TeamsView },
  { value: 'users', labelKey: 'nav.users', permission: 'users', component: UsersView },
  { value: 'roles', labelKey: 'nav.roles', permission: 'roles', component: RolesView },
]
</script>

<template>
  <SettingsTabHub :tabs="tabs" default-tab="teams" />
</template>
