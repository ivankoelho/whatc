<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Volume2 } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()
const { t } = useI18n()

const summary = computed(() => {
  const c = props.data?.config || {}
  if (c.greeting_text) return c.greeting_text.substring(0, 40) + (c.greeting_text.length > 40 ? '...' : '')
  if (c.audio_file) return c.audio_file
  return t('calling.nodes.noAudioConfigured')
})

const outputHandles = computed(() => [{ id: 'default', label: t('calling.nodes.next') }])
</script>

<template>
  <BaseNode :label="data?.label || t('calling.nodes.greeting')" header-class="bg-green-600" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Volume2 class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
    <p v-if="data?.config?.interruptible" class="text-[10px] text-green-600 mt-0.5">{{ t('calling.nodes.interruptible') }}</p>
  </BaseNode>
</template>
