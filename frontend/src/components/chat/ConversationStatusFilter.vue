<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ContactStatusFilter } from '@/stores/contacts'

const props = defineProps<{
  modelValue: ContactStatusFilter
  newCount: number
}>()

const emit = defineEmits<{ 'update:modelValue': [ContactStatusFilter] }>()

const { t } = useI18n()

const options = computed(() => [
  { value: 'all' as const, label: t('chat.filterAll') },
  { value: 'new' as const, label: t('chat.statusNew') },
  { value: 'in_progress' as const, label: t('chat.statusInProgress') },
  { value: 'resolved' as const, label: t('chat.statusResolved') }
])
</script>

<template>
  <!-- Scrolling pills rather than a 4-column segmented control: "Em andamento"
       alone does not fit the 320px sidebar without truncating, and truncation
       only gets worse in longer locales. Pills size to their label. -->
  <div
    class="flex items-center gap-1.5 overflow-x-auto px-2 pb-2 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
    role="tablist"
  >
    <button
      v-for="opt in options"
      :key="opt.value"
      role="tab"
      type="button"
      :aria-selected="props.modelValue === opt.value"
      :class="[
        'flex-shrink-0 inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium transition-colors',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-500',
        props.modelValue === opt.value
          ? 'bg-emerald-600 text-white'
          : 'bg-white/[0.08] text-white/70 hover:text-white hover:bg-white/[0.12] light:bg-gray-100 light:text-gray-600 light:hover:bg-gray-200 light:hover:text-gray-900'
      ]"
      @click="emit('update:modelValue', opt.value)"
    >
      {{ opt.label }}
      <span
        v-if="opt.value === 'new' && props.newCount > 0"
        class="inline-flex h-4 min-w-[16px] items-center justify-center rounded-full px-1 text-[10px]"
        :class="props.modelValue === 'new' ? 'bg-white/25 text-white' : 'bg-emerald-500 text-white'"
      >
        {{ props.newCount }}
      </span>
    </button>
  </div>
</template>
