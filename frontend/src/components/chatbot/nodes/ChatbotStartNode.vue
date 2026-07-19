<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Play } from 'lucide-vue-next'
import BaseNode from '@/components/calling/nodes/BaseNode.vue'

defineOptions({ inheritAttrs: false })
defineProps<{ data: any }>()
const { t } = useI18n()
</script>

<template>
  <BaseNode :label="data?.label || t('chatbot.nodes.start')" header-class="bg-emerald-600" :has-input="false">
    <template #icon><Play class="w-4 h-4" /></template>
    <p class="text-[10px] text-muted-foreground">{{ t('chatbot.nodes.flowEntryPoint') }}</p>
  </BaseNode>
</template>
