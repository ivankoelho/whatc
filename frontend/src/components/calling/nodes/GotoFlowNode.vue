<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ExternalLink } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'
import { useCallingStore } from '@/stores/calling'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()
const { t } = useI18n()
const callingStore = useCallingStore()

const summary = computed(() => {
  const flowId = props.data?.config?.flow_id
  if (!flowId) return t('calling.nodes.noFlow')
  const flow = callingStore.ivrFlows.find(f => f.id === flowId)
  return flow?.name || t('calling.nodes.noFlow')
})
</script>

<template>
  <BaseNode :label="data?.label || t('calling.nodes.gotoFlow')" header-class="bg-teal-600" :output-handles="[]" :has-input="!data?.isEntryNode">
    <template #icon><ExternalLink class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
