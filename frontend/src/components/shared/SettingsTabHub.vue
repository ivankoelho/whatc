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

// Tailwind's scanner needs literal class names — a template-literal
// `grid-cols-${n}` would compile to nothing since it never appears as a
// whole string in the source.
const GRID_COLS_CLASS: Record<number, string> = {
  1: 'grid-cols-1',
  2: 'grid-cols-2',
  3: 'grid-cols-3',
  4: 'grid-cols-4',
  5: 'grid-cols-5',
  6: 'grid-cols-6',
}

const visibleTabs = computed(() =>
  props.tabs.filter(tab => authStore.hasPermission(tab.permission, 'read')),
)

const gridColsClass = computed(() => GRID_COLS_CLASS[visibleTabs.value.length] ?? 'grid-cols-1')

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
  <Tabs v-else :model-value="activeTab ?? undefined" class="w-full" @update:model-value="onTabChange">
    <TabsList :class="['grid w-full mb-6 lg:w-auto lg:inline-flex', gridColsClass]">
      <TabsTrigger v-for="tab in visibleTabs" :key="tab.value" :value="tab.value">
        {{ t(tab.labelKey) }}
      </TabsTrigger>
    </TabsList>
    <TabsContent v-for="tab in visibleTabs" :key="tab.value" :value="tab.value">
      <component :is="tab.component" />
    </TabsContent>
  </Tabs>
</template>
