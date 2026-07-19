<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Clock } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: any }>()
const { t } = useI18n()

const summary = computed(() => {
  const cfg = props.data?.config?.input_config || props.data?.config || {}
  const schedule = (cfg.schedule as any[]) || []
  const active = schedule.filter((s) => s?.enabled).length
  return t('chatbot.nodes.daysActive', { active })
})

const outputHandles = computed(() => [
  { id: 'in_hours', label: t('chatbot.nodes.open'), title: t('chatbot.nodes.withinBusinessHours') },
  { id: 'out_of_hours', label: t('chatbot.nodes.closed'), title: t('chatbot.nodes.outsideBusinessHours') },
])
</script>

<template>
  <BaseNode
    :label="data?.label || t('chatbot.nodes.timing')"
    header-class="bg-cyan-600"
    :output-handles="outputHandles"
    :has-input="!data?.isEntryNode"
  >
    <template #icon><Clock class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
