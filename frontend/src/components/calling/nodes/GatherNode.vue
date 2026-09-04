<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Hash } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()
const { t } = useI18n()

const summary = computed(() => {
  const c = props.data?.config || {}
  const parts: string[] = []
  if (c.max_digits) parts.push(t('calling.nodes.digits', { count: c.max_digits }))
  if (c.store_as) parts.push(`→ ${c.store_as}`)
  return parts.join(', ') || t('calling.nodes.gatherInput')
})

const outputHandles = computed(() => [
  { id: 'default', label: t('calling.nodes.next') },
  { id: 'timeout', label: t('calling.nodes.timeout'), title: t('calling.nodes.timeoutTitle') },
  { id: 'max_retries', label: t('calling.nodes.maxRetries'), title: t('calling.nodes.maxRetriesTitle') },
])
</script>

<template>
  <BaseNode :label="data?.label || t('calling.nodes.gather')" header-class="bg-blue-600" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Hash class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
