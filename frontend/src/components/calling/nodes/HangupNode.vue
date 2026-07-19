<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { PhoneOff } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'

defineOptions({ inheritAttrs: false })

defineProps<{ data: Record<string, any> }>()
const { t } = useI18n()
</script>

<template>
  <BaseNode :label="data?.label || t('calling.nodes.hangup')" header-class="bg-red-600" :output-handles="[]" :has-input="!data?.isEntryNode">
    <template #icon><PhoneOff class="w-4 h-4" /></template>
    <p v-if="data?.config?.audio_file || data?.config?.greeting_text" class="truncate">
      {{ data.config.greeting_text || data.config.audio_file }}
    </p>
    <p v-else>{{ t('calling.nodes.endCall') }}</p>
  </BaseNode>
</template>
