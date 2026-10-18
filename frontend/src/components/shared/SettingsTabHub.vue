<script setup lang="ts">
import { computed, watch } from 'vue'
import type { Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuthStore } from '@/stores/auth'
import { resolveActiveTab, type SettingsTabConfig } from '@/lib/settings-tab-hub'

export interface HubTab extends SettingsTabConfig {
  labelKey: string
  icon: Component
  /** Shown only to super admins, whatever the role's permissions (the server enforces it too). */
  superAdminOnly?: boolean
  /** A second resource the tab also needs read on (e.g. units/departments lists are served under occurrences:read). */
  alsoNeeds?: string
  component: Component
}

const props = defineProps<{
  tabs: HubTab[]
  defaultTab: string
}>()

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const { t } = useI18n()

// Super-admin-only tabs drop out before anything else looks at the list.
const allowedTabs = computed(() =>
  props.tabs.filter(tab =>
    (!tab.superAdminOnly || authStore.user?.is_super_admin)
    && (!tab.alsoNeeds || authStore.hasPermission(tab.alsoNeeds, 'read'))),
)

const visibleTabs = computed(() =>
  allowedTabs.value.filter(tab => authStore.hasPermission(tab.permission, 'read')),
)

const activeTab = computed(() =>
  resolveActiveTab(
    allowedTabs.value,
    typeof route.query.tab === 'string' ? route.query.tab : undefined,
    props.defaultTab,
    (permission) => authStore.hasPermission(permission, 'read'),
  ),
)

// Keep the URL honest: if resolveActiveTab fell back to a different tab
// than the one requested (disallowed, unknown, or absent), reflect that
// in ?tab= so a shared link shows whoever opens it the tab that's
// actually displayed, not the one that was asked for.
watch(activeTab, (value) => {
  if (value && route.query.tab !== value) {
    router.replace({ query: { ...route.query, tab: value } })
  }
}, { immediate: true })

function onTabChange(value: string | number) {
  router.replace({ query: { ...route.query, tab: String(value) } })
}
</script>

<template>
  <div v-if="visibleTabs.length === 0" class="p-6 text-sm text-white/50 light:text-gray-500">
    {{ t('common.noAccessToSection') }}
  </div>
  <Tabs
    v-else
    :model-value="activeTab ?? undefined"
    orientation="vertical"
    class="flex h-full min-h-0 w-full"
    @update:model-value="onTabChange"
  >
    <!-- Bitrix-style vertical icon rail on the left; the active tab gets what is
         left, so each settings page keeps its own scroll area (they are all
         h-full + ScrollArea). -->
    <!-- No comment may sit between the root v-if and v-else: in dev builds Vue
         attaches it to the v-else branch, the root becomes a fragment, and the
         page <Transition mode="out-in"> never finishes leaving (blank page). -->
    <TabsList class="page-rail">
      <TabsTrigger
        v-for="tab in visibleTabs"
        :key="tab.value"
        :value="tab.value"
        :title="t(tab.labelKey)"
        class="page-rail-item"
      >
        <component :is="tab.icon" class="h-5 w-5" aria-hidden="true" />
        <span class="page-rail-label">{{ t(tab.labelKey) }}</span>
      </TabsTrigger>
    </TabsList>
    <TabsContent v-for="tab in visibleTabs" :key="tab.value" :value="tab.value" class="mt-0 h-full min-h-0 min-w-0 flex-1">
      <component :is="tab.component" />
    </TabsContent>
  </Tabs>
</template>
