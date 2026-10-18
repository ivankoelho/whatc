<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowLeft, Hash, ShieldCheck } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/shared'
import ChannelComposition from '@/components/team-chat/ChannelComposition.vue'
import { useTeamChatStore } from '@/stores/teamChat'
import { useAuthStore } from '@/stores/auth'

// Administration of free channels: who is in each one and its state, never what is
// said in it (the server returns no messages, previews or unread counts here, and
// neither DMs nor team channels). Someone who manages the composition does not have to
// be in a channel to administer it; entering is a separate, audited action that needs
// team_chat:join_any and a reason that the members will see.
const { t } = useI18n()
const router = useRouter()
const store = useTeamChatStore()
const auth = useAuthStore()

const canManage = computed(() => auth.hasPermission('team_chat', 'manage'))
const canJoinAny = computed(() => auth.hasPermission('team_chat', 'join_any'))
const myId = computed(() => auth.user?.id)

onMounted(() => { void store.fetchAdminChannels() })

const isIn = (members: { user_id: string }[]) => members.some(m => m.user_id === myId.value)
</script>

<template>
  <div class="mx-auto flex h-full w-full max-w-3xl flex-col gap-4 overflow-y-auto p-4">
    <header class="flex items-center gap-2">
      <Button variant="ghost" size="icon" class="h-9 w-9" :aria-label="t('common.back')" @click="router.push({ name: 'team-chat' })">
        <ArrowLeft class="h-4 w-4" />
      </Button>
      <ShieldCheck class="h-5 w-5 text-muted-foreground" />
      <div>
        <h1 class="text-base font-semibold">{{ t('teamChat.admin.title') }}</h1>
        <p class="text-xs text-muted-foreground">{{ t('teamChat.admin.subtitle') }}</p>
      </div>
    </header>

    <ErrorState
      v-if="store.adminError && store.adminChannels.length === 0"
      :title="t('teamChat.loadFailedTitle')"
      :description="t('teamChat.loadFailed')"
      :retry-label="t('common.retry')"
      @retry="store.fetchAdminChannels()"
    />
    <div v-else-if="store.adminLoading && store.adminChannels.length === 0" class="space-y-3">
      <Skeleton v-for="i in 3" :key="i" class="h-40 w-full" />
    </div>
    <EmptyState
      v-else-if="store.adminChannels.length === 0"
      :icon="Hash"
      :title="t('teamChat.admin.emptyTitle')"
      :description="t('teamChat.admin.empty')"
    />

    <section
      v-for="c in store.adminChannels"
      :key="c.id"
      :data-testid="`admin-channel-${c.id}`"
      class="rounded-lg border bg-card p-4"
    >
      <div class="mb-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <Hash class="h-3.5 w-3.5" />
        <span v-if="c.created_by_name">{{ t('teamChat.admin.createdBy', { name: c.created_by_name }) }}</span>
        <Badge v-if="c.archived_at" variant="secondary">{{ t('teamChat.manage.archivedBadge') }}</Badge>
        <Badge v-if="isIn(c.members)" variant="outline">{{ t('teamChat.admin.youAreIn') }}</Badge>
      </div>
      <ChannelComposition
        :channel-id="c.id"
        :name="c.name"
        :archived-at="c.archived_at"
        :members="c.members"
        :can-compose="canManage"
        :can-reopen="canManage"
        :can-join="canJoinAny && !isIn(c.members)"
        :is-member="isIn(c.members)"
        :me-id="myId"
      />
    </section>
  </div>
</template>
