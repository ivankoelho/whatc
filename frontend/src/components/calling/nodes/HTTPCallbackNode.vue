<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Globe } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()
const { t } = useI18n()

const summary = computed(() => {
  const c = props.data?.config || {}
  const method = c.method || 'GET'
  const url = c.url || t('calling.nodes.noUrl')
  return `${method} ${url}`
})

const outputHandles = computed(() => [
  { id: 'http:2xx', label: t('calling.nodes.http2xx'), title: t('calling.nodes.http2xxTitle') },
  { id: 'http:non2xx', label: t('calling.nodes.httpError'), title: t('calling.nodes.httpErrorTitle') },
])
</script>

<template>
  <BaseNode :label="data?.label || t('calling.nodes.httpCallback')" header-class="bg-orange-600" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Globe class="w-4 h-4" /></template>
    <p class="truncate font-mono text-[10px]">{{ summary }}</p>
  </BaseNode>
</template>
