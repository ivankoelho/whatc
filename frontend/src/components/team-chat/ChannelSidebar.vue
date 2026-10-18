<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Hash, Users, Plus, MessageCircle, Archive, ShieldCheck } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { ScrollArea } from '@/components/ui/scroll-area'
import { ErrorState } from '@/components/shared'
import { useTeamChatStore } from '@/stores/teamChat'
import { useAuthStore } from '@/stores/auth'
import type { TeamChatChannel } from '@/services/api'

const props = defineProps<{ activeId: string | null }>()
const emit = defineEmits<{
  select: [channelId: string]
  newChannel: []
  newDm: []
  admin: []
}>()

const { t } = useI18n()
const store = useTeamChatStore()
const auth = useAuthStore()

// Buttons mirror what the server allows; the server still decides.
const canManage = computed(() => auth.hasPermission('team_chat', 'manage'))
const canWrite = computed(() => auth.hasPermission('team_chat', 'write'))

// "No channels" only when there is truly nothing to list: someone whose only channels
// are archived, with the archived list turned on, still has something to open.
const nothingToShow = computed(() =>
  store.channels.length === 0 && !(store.showArchived && store.archived.length > 0))

const sections = computed(() => [
  { key: 'team', label: t('teamChat.sectionTeams'), items: store.channels.filter(c => c.kind === 'team') },
  { key: 'channel', label: t('teamChat.sectionChannels'), items: store.channels.filter(c => c.kind === 'channel') },
  { key: 'dm', label: t('teamChat.sectionDirect'), items: store.channels.filter(c => c.kind === 'dm') },
  // Archived channels only show when asked for; they stay readable by their members.
  ...(store.showArchived ? [{ key: 'archived', label: t('teamChat.sectionArchived'), items: store.archived }] : [])
])

// The id is the identity, not the name: channels may share a name, so when one does
// the list adds who created it and how many people are in it.
const repeatedNames = computed(() => {
  const count: Record<string, number> = {}
  for (const c of [...store.channels, ...store.archived]) {
    if (c.kind === 'channel') count[c.name.toLowerCase()] = (count[c.name.toLowerCase()] ?? 0) + 1
  }
  return new Set(Object.keys(count).filter(k => count[k] > 1))
})
const isRepeated = (c: TeamChatChannel) => c.kind === 'channel' && repeatedNames.value.has(c.name.toLowerCase())

function isOnline(c: TeamChatChannel): boolean {
  return !!c.dm_user_id && !!store.users.find(u => u.id === c.dm_user_id)?.is_online
}

function badge(n: number): string {
  return n > 99 ? '99+' : String(n)
}
</script>

<template>
  <aside class="flex h-full w-full flex-col border-r bg-card md:w-72 md:shrink-0" :aria-label="t('teamChat.title')">
    <div class="flex items-center justify-between border-b px-4 py-3">
      <h2 class="text-sm font-semibold">{{ t('teamChat.title') }}</h2>
      <div class="flex gap-1">
        <Button
          v-if="canManage"
          variant="ghost"
          size="icon"
          class="h-7 w-7"
          :title="t('teamChat.admin.title')"
          :aria-label="t('teamChat.admin.title')"
          data-testid="open-admin"
          @click="emit('admin')"
        >
          <ShieldCheck class="h-4 w-4" />
        </Button>
        <Button
          v-if="canWrite"
          variant="ghost"
          size="icon"
          class="h-7 w-7"
          :title="t('teamChat.newDm')"
          :aria-label="t('teamChat.newDm')"
          @click="emit('newDm')"
        >
          <MessageCircle class="h-4 w-4" />
        </Button>
        <Button
          v-if="canManage"
          variant="ghost"
          size="icon"
          class="h-7 w-7"
          :title="t('teamChat.newChannel')"
          :aria-label="t('teamChat.newChannel')"
          @click="emit('newChannel')"
        >
          <Plus class="h-4 w-4" />
        </Button>
      </div>
    </div>

    <ErrorState
      v-if="store.channelsError && store.channels.length === 0"
      :title="t('teamChat.loadFailedTitle')"
      :description="t('teamChat.loadFailed')"
      :retry-label="t('common.retry')"
      @retry="store.fetchChannels()"
    />

    <div v-else-if="store.channelsLoading && store.channels.length === 0" class="space-y-2 p-3">
      <Skeleton v-for="i in 5" :key="i" class="h-9 w-full" />
    </div>

    <ScrollArea v-else class="flex-1">
      <p v-if="nothingToShow" class="px-4 py-8 text-center text-sm text-muted-foreground">
        {{ t('teamChat.noChannels') }}
      </p>
      <nav v-else class="space-y-4 p-2">
        <section v-for="s in sections" :key="s.key" v-show="s.items.length > 0">
          <h3 class="px-2 pb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">{{ s.label }}</h3>
          <ul>
            <li v-for="c in s.items" :key="c.id">
              <button
                type="button"
                :data-testid="`channel-${c.id}`"
                :class="[
                  'flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors hover:bg-muted',
                  props.activeId === c.id ? 'bg-muted font-medium' : ''
                ]"
                @click="emit('select', c.id)"
              >
                <Users v-if="c.kind === 'team'" class="h-4 w-4 shrink-0 text-muted-foreground" />
                <Hash v-else-if="c.kind === 'channel'" class="h-4 w-4 shrink-0 text-muted-foreground" />
                <span
                  v-else
                  :class="['h-2.5 w-2.5 shrink-0 rounded-full', isOnline(c) ? 'bg-emerald-500' : 'bg-muted-foreground/40']"
                  :title="isOnline(c) ? t('teamChat.online') : t('teamChat.offline')"
                />
                <span class="min-w-0 flex-1">
                  <span class="block truncate">{{ c.name || t('teamChat.unnamed') }}</span>
                  <span v-if="isRepeated(c)" class="block truncate text-[11px] text-muted-foreground" :data-testid="`homonym-${c.id}`">
                    {{ t('teamChat.sameName', { name: c.created_by_name || '-', n: c.member_count ?? 0 }) }}
                  </span>
                </span>
                <span
                  v-if="c.unread_count > 0"
                  class="min-w-5 rounded-full bg-primary px-1.5 text-center text-[11px] font-semibold leading-5 text-primary-foreground"
                  :aria-label="t('teamChat.unreadCount', { n: c.unread_count })"
                >{{ badge(c.unread_count) }}</span>
              </button>
            </li>
          </ul>
        </section>
      </nav>
    </ScrollArea>

    <div class="border-t p-2">
      <Button
        variant="ghost"
        size="sm"
        class="h-8 w-full justify-start gap-2 text-xs text-muted-foreground"
        :aria-pressed="store.showArchived"
        data-testid="toggle-archived"
        @click="store.setShowArchived(!store.showArchived)"
      >
        <Archive class="h-3.5 w-3.5" />
        {{ store.showArchived ? t('teamChat.hideArchived') : t('teamChat.showArchived') }}
      </Button>
    </div>
  </aside>
</template>
