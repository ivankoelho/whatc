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

const visibleTabs = computed(() =>
  props.tabs.filter(tab => authStore.hasPermission(tab.permission, 'read')),
)

const activeTab = computed(() =>
  resolveActiveTab(
    props.tabs,
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
  <!-- The hub fills the page height and hands what is left under the tab strip
       to the active tab, so each settings page keeps its own scroll area
       (they are all h-full + ScrollArea). -->
  <Tabs v-else :model-value="activeTab ?? undefined" class="flex h-full min-h-0 w-full flex-col" @update:model-value="onTabChange">
    <TabsList class="h-auto w-full shrink-0 justify-start gap-6 overflow-x-auto rounded-none border-b border-white/[0.08] bg-transparent p-0 px-6 light:border-gray-200">
      <TabsTrigger
        v-for="tab in visibleTabs"
        :key="tab.value"
        :value="tab.value"
        class="-mb-px h-11 rounded-none border-b-2 border-transparent bg-transparent px-0 text-[13px] text-white/50 shadow-none hover:text-white/80 data-[state=active]:border-emerald-500 data-[state=active]:bg-transparent data-[state=active]:text-white data-[state=active]:shadow-none light:text-gray-500 light:hover:text-gray-800 light:data-[state=active]:text-gray-900"
      >
        {{ t(tab.labelKey) }}
      </TabsTrigger>
    </TabsList>
    <TabsContent v-for="tab in visibleTabs" :key="tab.value" :value="tab.value" class="mt-0 min-h-0 flex-1">
      <component :is="tab.component" />
    </TabsContent>
  </Tabs>
</template>
