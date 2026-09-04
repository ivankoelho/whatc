<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Clock } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()
const { t } = useI18n()

const summary = computed(() => {
  const c = props.data?.config || {}
  const schedule = c.schedule || []
  const activeDays = schedule.filter((s: any) => s.enabled).length
  return t('calling.nodes.daysActive', { count: activeDays })
})

const outputHandles = computed(() => [
  { id: 'in_hours', label: t('calling.nodes.open'), title: t('calling.nodes.openTitle') },
  { id: 'out_of_hours', label: t('calling.nodes.closed'), title: t('calling.nodes.closedTitle') },
])
</script>

<template>
  <BaseNode :label="data?.label || t('calling.nodes.timing')" header-class="bg-cyan-600" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Clock class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
