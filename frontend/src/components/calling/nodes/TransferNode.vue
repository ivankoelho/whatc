<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Users } from 'lucide-vue-next'
import BaseNode from './BaseNode.vue'
import { useTeamsStore } from '@/stores/teams'

defineOptions({ inheritAttrs: false })

const props = defineProps<{ data: Record<string, any> }>()
const { t } = useI18n()
const teamsStore = useTeamsStore()

const summary = computed(() => {
  const teamId = props.data?.config?.team_id
  if (!teamId) return t('calling.nodes.noTeam')
  const team = teamsStore.teams.find(tm => tm.id === teamId)
  return team?.name || t('calling.nodes.noTeam')
})

const outputHandles = computed(() => [
  { id: 'completed', label: t('calling.nodes.completed') },
  { id: 'no_answer', label: t('calling.nodes.noAnswer') },
])
</script>

<template>
  <BaseNode :label="data?.label || t('calling.nodes.transfer')" header-class="bg-amber-600" :output-handles="outputHandles" :has-input="!data?.isEntryNode">
    <template #icon><Users class="w-4 h-4" /></template>
    <p class="truncate">{{ summary }}</p>
  </BaseNode>
</template>
