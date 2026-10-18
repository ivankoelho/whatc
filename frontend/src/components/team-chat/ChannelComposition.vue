<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Archive, ArchiveRestore, Crown, LogOut, Pencil, Plus, ShieldAlert, UserMinus, Check, X } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import PickPersonDialog from './PickPersonDialog.vue'
import AdminJoinDialog from './AdminJoinDialog.vue'
import { useTeamChatStore } from '@/stores/teamChat'
import { chatErrorCode } from '@/lib/team-chat'
import type { TeamChatMember } from '@/services/api'

// Who is in a free channel and what can be done about it. Used inside the channel
// (for its members) and in the administration screen (for people who manage the
// composition without being members). The flags mirror what the server allows; the
// server still decides, and its refusal is explained here.
const props = defineProps<{
  channelId: string
  name: string
  archivedAt?: string
  members: TeamChatMember[]
  /** owner of the channel, or team_chat:manage */
  canCompose: boolean
  /** team_chat:manage */
  canReopen: boolean
  /** team_chat:join_any and not a member */
  canJoin: boolean
  /** the caller is a member */
  isMember: boolean
  meId?: string
}>()
const emit = defineEmits<{ left: [] }>()

const { t } = useI18n()
const store = useTeamChatStore()

const archived = computed(() => !!props.archivedAt)
const pickOpen = ref(false)
const joinOpen = ref(false)
const renaming = ref(false)
const draftName = ref(props.name)
const busy = ref(false)

watch(() => props.name, n => { if (!renaming.value) draftName.value = n })

const memberIds = computed(() => props.members.map(m => m.user_id))

function explain(err: unknown) {
  const code = chatErrorCode(err)
  const known = ['last_owner', 'archived', 'forbidden', 'self_add', 'already_member', 'not_archived', 'not_found', 'invalid']
  toast.error(t(known.includes(code) ? `teamChat.errors.${code}` : 'teamChat.errors.generic'))
}

async function run(fn: () => Promise<unknown>, okKey?: string) {
  if (busy.value) return
  busy.value = true
  try {
    await fn()
    if (okKey) toast.success(t(okKey))
    return true
  } catch (err) {
    explain(err)
    return false
  } finally {
    busy.value = false
  }
}

async function addPerson(userId: string) {
  pickOpen.value = false
  await run(() => store.addMember(props.channelId, userId), 'teamChat.manage.added')
}

async function remove(m: TeamChatMember) {
  if (!window.confirm(t('teamChat.manage.confirmRemove', { name: m.name }))) return
  await run(() => store.removeMember(props.channelId, m.user_id), 'teamChat.manage.removed')
}

async function toggleRole(m: TeamChatMember) {
  await run(() => store.setRole(props.channelId, m.user_id, m.role === 'owner' ? 'member' : 'owner'))
}

async function leave() {
  if (!window.confirm(t('teamChat.manage.confirmLeave'))) return
  if (await run(() => store.leaveChannel(props.channelId), 'teamChat.manage.left')) emit('left')
}

async function saveName() {
  const name = draftName.value.trim()
  if (!name || name === props.name) {
    renaming.value = false
    return
  }
  if (await run(() => store.renameChannel(props.channelId, name), 'teamChat.manage.renamed')) renaming.value = false
}

async function archive() {
  if (!window.confirm(t('teamChat.manage.confirmArchive'))) return
  await run(() => store.archiveChannel(props.channelId), 'teamChat.manage.archivedOk')
}

async function reopen() {
  await run(() => store.reopenChannel(props.channelId), 'teamChat.manage.reopenedOk')
}
</script>

<template>
  <div class="space-y-5" data-testid="channel-composition">
    <!-- Name -->
    <div class="space-y-1.5">
      <h3 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">{{ t('teamChat.manage.name') }}</h3>
      <form v-if="renaming" class="flex gap-2" @submit.prevent="saveName">
        <Input v-model="draftName" maxlength="100" :aria-label="t('teamChat.channelName')" autocomplete="off" />
        <Button type="submit" size="icon" class="shrink-0" :aria-label="t('common.save')" :disabled="busy"><Check class="h-4 w-4" /></Button>
        <Button type="button" variant="ghost" size="icon" class="shrink-0" :aria-label="t('common.cancel')" @click="renaming = false"><X class="h-4 w-4" /></Button>
      </form>
      <div v-else class="flex items-center gap-2">
        <span class="min-w-0 flex-1 truncate text-sm font-medium">{{ name }}</span>
        <Badge v-if="archived" variant="secondary">{{ t('teamChat.manage.archivedBadge') }}</Badge>
        <Button
          v-if="canCompose && !archived"
          variant="ghost" size="icon" class="h-9 w-9 md:h-7 md:w-7"
          :aria-label="t('teamChat.manage.rename')" @click="renaming = true; draftName = name"
        ><Pencil class="h-3.5 w-3.5" /></Button>
      </div>
    </div>

    <!-- Members -->
    <div class="space-y-1.5">
      <div class="flex items-center justify-between">
        <h3 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {{ t('teamChat.manage.members', { n: members.length }) }}
        </h3>
        <Button v-if="canCompose && !archived" variant="outline" size="sm" class="h-9 text-xs md:h-7" @click="pickOpen = true">
          <Plus class="mr-1 h-3 w-3" />{{ t('teamChat.manage.add') }}
        </Button>
      </div>
      <ul class="divide-y rounded-md border">
        <li v-if="members.length === 0" class="px-3 py-3 text-sm text-muted-foreground">{{ t('teamChat.manage.noMembers') }}</li>
        <li v-for="m in members" :key="m.user_id" :data-testid="`member-${m.user_id}`" class="flex items-center gap-2 px-3 py-2">
          <span :class="['h-2 w-2 shrink-0 rounded-full', m.is_online ? 'bg-emerald-500' : 'bg-muted-foreground/40']" />
          <span class="min-w-0 flex-1 truncate text-sm">
            {{ m.name }}<span v-if="m.user_id === meId" class="text-muted-foreground"> ({{ t('teamChat.manage.you') }})</span>
          </span>
          <Badge v-if="m.role === 'owner'" variant="secondary" class="gap-1"><Crown class="h-3 w-3" />{{ t('teamChat.manage.owner') }}</Badge>
          <template v-if="canCompose && m.user_id !== meId">
            <Button
              v-if="!archived" variant="ghost" size="icon" class="h-9 w-9 md:h-7 md:w-7"
              :aria-label="m.role === 'owner' ? t('teamChat.manage.demote', { name: m.name }) : t('teamChat.manage.promote', { name: m.name })"
              :disabled="busy" @click="toggleRole(m)"
            ><Crown class="h-3.5 w-3.5" /></Button>
            <Button
              variant="ghost" size="icon" class="h-9 w-9 md:h-7 md:w-7 text-destructive"
              :aria-label="t('teamChat.manage.remove', { name: m.name })" :disabled="busy" @click="remove(m)"
            ><UserMinus class="h-3.5 w-3.5" /></Button>
          </template>
        </li>
      </ul>
    </div>

    <!-- Actions -->
    <div class="flex flex-wrap gap-2">
      <Button v-if="isMember" variant="outline" size="sm" :disabled="busy" @click="leave">
        <LogOut class="mr-1 h-3.5 w-3.5" />{{ t('teamChat.manage.leave') }}
      </Button>
      <Button v-if="canCompose && !archived" variant="outline" size="sm" :disabled="busy" @click="archive">
        <Archive class="mr-1 h-3.5 w-3.5" />{{ t('teamChat.manage.archive') }}
      </Button>
      <Button v-if="canReopen && archived" variant="outline" size="sm" :disabled="busy" @click="reopen">
        <ArchiveRestore class="mr-1 h-3.5 w-3.5" />{{ t('teamChat.manage.reopen') }}
      </Button>
      <Button v-if="canJoin && !archived" variant="outline" size="sm" :disabled="busy" @click="joinOpen = true">
        <ShieldAlert class="mr-1 h-3.5 w-3.5" />{{ t('teamChat.admin.joinAction') }}
      </Button>
    </div>

    <PickPersonDialog
      v-model:open="pickOpen"
      :title="t('teamChat.manage.add')"
      :exclude-ids="memberIds"
      @pick="addPerson"
    />
    <AdminJoinDialog v-model:open="joinOpen" :channel-id="channelId" :channel-name="name" />
  </div>
</template>
