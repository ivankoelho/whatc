<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ExternalLink } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: any }>()
const { t } = useI18n()

const summary = computed(() => {
  const cfg = props.data?.config?.input_config || props.data?.config || {}
  const flowName = cfg.flow_name as string | undefined
  if (flowName) return `→ ${flowName}`
  if (cfg.flow_id) return `→ flow ${(cfg.flow_id as string).slice(0, 8)}…`
  return t('chatbot.nodes.noTargetFlow')
})
</script>

<template>
  <BaseNode
    :label="data?.label || t('chatbot.nodes.gotoFlow')"
    header-class="bg-teal-600"
    :output-handles="[]"
    :has-input="!data?.isEntryNode"
  >
    <template #icon><ExternalLink class="w-4 h-4" /></template>
    <p class="truncate" :title="summary">{{ summary }}</p>
  </BaseNode>
</template>
