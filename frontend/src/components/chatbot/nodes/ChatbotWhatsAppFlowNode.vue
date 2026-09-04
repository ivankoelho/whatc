<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessageCircle } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: any }>()
const { t } = useI18n()

const summary = computed(() => {
  const cfg = props.data?.config || {}
  return cfg.cta || cfg.cta_text || cfg.flow_name || cfg.body || t('chatbot.nodes.whatsappFlow')
})
</script>

<template>
  <BaseNode :label="data?.label || t('chatbot.nodes.whatsappFlow')" header-class="bg-green-600" :has-input="!data?.isEntryNode">
    <template #icon><MessageCircle class="w-4 h-4" /></template>
    <p class="truncate" :title="summary">{{ summary }}</p>
  </BaseNode>
</template>
