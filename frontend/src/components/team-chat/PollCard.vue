<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import type { TeamChatMessage } from '@/services/api'
import { useTeamChatStore } from '@/stores/teamChat'
import { chatErrorCode } from '@/lib/team-chat'

// The options of a poll with their tally. A vote replaces the caller's previous choice; the server
// is the source of truth, so the card shows what it answered. Names of voters are plain text.
const props = defineProps<{ message: TeamChatMessage; canVote: boolean }>()
const { t } = useI18n()
const store = useTeamChatStore()
const busy = ref(false)

const poll = computed(() => props.message.poll!)
const share = (count: number) => (poll.value.voters ? Math.round((count * 100) / poll.value.voters) : 0)

async function vote(optionId: string) {
  if (!props.canVote || busy.value) return
  const mine = poll.value.options.filter(o => o.mine).map(o => o.id)
  let next: string[]
  if (poll.value.multiple) next = mine.includes(optionId) ? mine.filter(id => id !== optionId) : [...mine, optionId]
  else next = mine.includes(optionId) ? [] : [optionId]
  busy.value = true
  try {
    await store.votePoll(props.message, next)
  } catch (err) {
    const c = chatErrorCode(err)
    toast.error(t(['archived', 'forbidden', 'not_found'].includes(c) ? `teamChat.errors.${c}` : 'teamChat.errors.generic'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="mt-1 w-full max-w-md space-y-2 rounded-lg border bg-background/60 p-3" :data-testid="`poll-${message.id}`">
    <p class="text-sm font-semibold [overflow-wrap:anywhere]">{{ message.body }}</p>
    <p class="text-xs text-muted-foreground">{{ poll.multiple ? t('teamChat.poll.multiple') : t('teamChat.poll.single') }}</p>
    <ul class="space-y-1.5">
      <li v-for="o in poll.options" :key="o.id">
        <button
          type="button"
          class="relative w-full overflow-hidden rounded-md border px-3 py-2 text-left text-sm transition-colors hover:bg-muted disabled:cursor-default disabled:hover:bg-transparent"
          :class="o.mine ? 'border-primary' : ''"
          :disabled="!canVote || busy"
          :aria-pressed="o.mine"
          :data-testid="`poll-option-${o.id}`"
          @click="vote(o.id)"
        >
          <span class="absolute inset-y-0 left-0 bg-primary/15" :style="{ width: share(o.count) + '%' }" aria-hidden="true" />
          <span class="relative flex items-center justify-between gap-2">
            <span class="flex min-w-0 items-center gap-2">
              <Check v-if="o.mine" class="h-4 w-4 shrink-0 text-primary" aria-hidden="true" />
              <span class="[overflow-wrap:anywhere]">{{ o.label }}</span>
            </span>
            <span class="shrink-0 text-xs text-muted-foreground" :title="o.voters.join(', ')">{{ o.count }} · {{ share(o.count) }}%</span>
          </span>
        </button>
      </li>
    </ul>
    <p class="text-xs text-muted-foreground">{{ t('teamChat.poll.voters', { n: poll.voters }) }}</p>
  </div>
</template>
