<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Grid3X3 } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()
const { t } = useI18n()

const options = computed(() => {
  const opts = props.data?.config?.options || {}
  return Object.entries(opts).sort(([a], [b]) => a.localeCompare(b))
})

const outputHandles = computed(() => {
  const handles: { id: string; label: string; title?: string }[] = []
  for (const [digit] of options.value) {
    handles.push({ id: `digit:${digit}`, label: digit })
  }
  handles.push({ id: 'timeout', label: t('calling.nodes.timeout'), title: t('calling.nodes.timeoutTitle') })
  handles.push({ id: 'max_retries', label: t('calling.nodes.maxRetries'), title: t('calling.nodes.maxRetriesTitle') })
  return handles
})
</script>

<template>
  <BaseNode :label="data?.label || t('calling.nodes.menu')" header-class="bg-purple-600" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Grid3X3 class="w-4 h-4" /></template>
    <div v-if="options.length > 0" class="space-y-0.5">
      <div v-for="[digit, opt] in options" :key="digit" class="flex gap-1">
        <span class="font-mono font-bold">{{ digit }}:</span>
        <span class="truncate">{{ (opt as any)?.label || '—' }}</span>
      </div>
    </div>
    <p v-else class="text-muted-foreground">{{ t('calling.nodes.noOptions') }}</p>
  </BaseNode>
</template>
