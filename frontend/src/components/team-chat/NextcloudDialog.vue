<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight, File as FileIcon, Folder, Loader2 } from 'lucide-vue-next'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { nextcloudService, type NextcloudEntry, type NextcloudStatus } from '@/services/api'
import { useTeamChatStore } from '@/stores/teamChat'
import { chatErrorCode } from '@/lib/team-chat'

// Pick a file from the person's own Nextcloud. Three states, one dialog: the organization has no
// server yet (an administrator registers it; anyone else is told to ask), the person has not
// connected (user name + app password, which Nextcloud lets them revoke), or the browser.
// With a channelId the file goes to that chat; without one the dialog only reports the choice
// (the case documents use it that way and attach it themselves).
const props = defineProps<{ open: boolean; channelId?: string }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; picked: [entry: NextcloudEntry] }>()
const { t } = useI18n()
const store = useTeamChatStore()

const status = ref<NextcloudStatus | null>(null)
const loading = ref(false)
const busy = ref(false)
const baseUrl = ref('')
const username = ref('')
const appPassword = ref('')
const path = ref('/')
const entries = ref<NextcloudEntry[]>([])
const unwrap = <T,>(res: { data: unknown }): T => ((res.data as { data?: unknown }).data ?? res.data) as T

function fail(err: unknown) {
  const c = chatErrorCode(err)
  const known = ['nextcloud_not_configured', 'nextcloud_not_connected', 'nextcloud_auth', 'nextcloud_unreachable', 'nextcloud_bad_url', 'nextcloud_bad_path', 'nextcloud_not_found', 'file_too_large', 'file_type_not_allowed', 'rate_limited', 'archived', 'not_found']
  toast.error(t(known.includes(c) ? `teamChat.nextcloud.errors.${c}` : 'teamChat.errors.generic'))
}

async function load() {
  loading.value = true
  try {
    status.value = unwrap<NextcloudStatus>(await nextcloudService.status())
    if (status.value.configured && status.value.connected) await browse('/')
  } catch (err) {
    fail(err)
  } finally {
    loading.value = false
  }
}

async function browse(p: string) {
  busy.value = true
  try {
    const res = unwrap<{ path: string; entries: NextcloudEntry[] }>(await nextcloudService.files(p))
    path.value = res.path
    entries.value = res.entries
  } catch (err) {
    if (chatErrorCode(err) === 'nextcloud_auth') status.value = status.value && { ...status.value, connected: false }
    fail(err)
  } finally {
    busy.value = false
  }
}

watch(() => props.open, open => {
  if (open) {
    baseUrl.value = username.value = appPassword.value = ''
    path.value = '/'
    entries.value = []
    void load()
  }
}, { immediate: true })

async function saveServer() {
  busy.value = true
  try {
    await nextcloudService.saveServer(baseUrl.value.trim())
    await load()
  } catch (err) {
    fail(err)
  } finally {
    busy.value = false
  }
}

async function connect() {
  busy.value = true
  try {
    await nextcloudService.connect(username.value.trim(), appPassword.value)
    appPassword.value = ''
    await load()
  } catch (err) {
    fail(err)
  } finally {
    busy.value = false
  }
}

async function disconnect() {
  try {
    await nextcloudService.disconnect()
    await load()
  } catch (err) {
    fail(err)
  }
}

async function pick(e: NextcloudEntry) {
  if (e.is_dir) return browse(e.path)
  if (!props.channelId) {
    emit('picked', e)
    return
  }
  busy.value = true
  try {
    await store.attachNextcloud(props.channelId, e.path)
    toast.success(t('teamChat.nextcloud.attached', { name: e.name }))
    emit('update:open', false)
  } catch (err) {
    fail(err)
  } finally {
    busy.value = false
  }
}

const crumbs = () => {
  const parts = path.value.split('/').filter(Boolean)
  return parts.map((name, i) => ({ name, path: '/' + parts.slice(0, i + 1).join('/') }))
}
const sizeLabel = (n: number) => (n >= 1048576 ? `${(n / 1048576).toFixed(1)} MB` : n >= 1024 ? `${Math.round(n / 1024)} KB` : n ? `${n} B` : '')
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="max-w-lg">
      <DialogHeader>
        <DialogTitle>{{ t('teamChat.nextcloud.title') }}</DialogTitle>
        <DialogDescription>{{ t('teamChat.nextcloud.hint') }}</DialogDescription>
      </DialogHeader>

      <div v-if="loading" class="flex justify-center py-8"><Loader2 class="h-6 w-6 animate-spin text-muted-foreground" /></div>

      <!-- No server for the organization yet -->
      <div v-else-if="status && !status.configured" class="space-y-3" data-testid="nextcloud-unconfigured">
        <template v-if="status.can_configure">
          <p class="text-sm text-muted-foreground">{{ t('teamChat.nextcloud.setupHint') }}</p>
          <div class="space-y-1.5">
            <Label for="nc-url">{{ t('teamChat.nextcloud.serverUrl') }}</Label>
            <Input id="nc-url" v-model="baseUrl" placeholder="https://nextcloud.suaempresa.com.br" data-testid="nextcloud-url" />
          </div>
          <div class="flex justify-end">
            <Button :disabled="busy || !baseUrl.trim()" data-testid="nextcloud-save-server" @click="saveServer">
              <Loader2 v-if="busy" class="mr-2 h-4 w-4 animate-spin" />{{ t('common.save') }}
            </Button>
          </div>
        </template>
        <p v-else class="text-sm text-muted-foreground">{{ t('teamChat.nextcloud.askAdmin') }}</p>
      </div>

      <!-- Server known, this person not connected -->
      <form v-else-if="status && !status.connected" class="space-y-3" data-testid="nextcloud-connect" @submit.prevent="connect">
        <p class="text-sm text-muted-foreground">{{ t('teamChat.nextcloud.connectHint', { url: status.base_url }) }}</p>
        <div class="space-y-1.5">
          <Label for="nc-user">{{ t('teamChat.nextcloud.username') }}</Label>
          <Input id="nc-user" v-model="username" autocomplete="off" data-testid="nextcloud-user" />
        </div>
        <div class="space-y-1.5">
          <Label for="nc-pass">{{ t('teamChat.nextcloud.appPassword') }}</Label>
          <Input id="nc-pass" v-model="appPassword" type="password" autocomplete="off" data-testid="nextcloud-pass" />
          <p class="text-xs text-muted-foreground">{{ t('teamChat.nextcloud.appPasswordHint') }}</p>
        </div>
        <div class="flex justify-end">
          <Button type="submit" :disabled="busy || !username.trim() || !appPassword" data-testid="nextcloud-connect-submit">
            <Loader2 v-if="busy" class="mr-2 h-4 w-4 animate-spin" />{{ t('teamChat.nextcloud.connect') }}
          </Button>
        </div>
      </form>

      <!-- Browser -->
      <div v-else-if="status" class="space-y-2" data-testid="nextcloud-browser">
        <nav class="flex flex-wrap items-center gap-1 text-sm" :aria-label="t('teamChat.nextcloud.path')">
          <button type="button" class="rounded px-1 hover:bg-muted" @click="browse('/')">{{ t('teamChat.nextcloud.root') }}</button>
          <template v-for="c in crumbs()" :key="c.path">
            <ChevronRight class="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
            <button type="button" class="rounded px-1 hover:bg-muted" @click="browse(c.path)">{{ c.name }}</button>
          </template>
        </nav>
        <ul class="max-h-80 divide-y overflow-y-auto rounded-md border" :class="busy ? 'opacity-60' : ''">
          <li v-if="entries.length === 0" class="px-3 py-4 text-sm text-muted-foreground">{{ t('teamChat.nextcloud.empty') }}</li>
          <li v-for="e in entries" :key="e.path">
            <button type="button" class="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-muted" :disabled="busy" :data-testid="`nextcloud-entry-${e.name}`" @click="pick(e)">
              <Folder v-if="e.is_dir" class="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              <FileIcon v-else class="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              <span class="min-w-0 flex-1 truncate">{{ e.name }}</span>
              <span class="shrink-0 text-xs text-muted-foreground">{{ e.is_dir ? '' : sizeLabel(e.size) }}</span>
            </button>
          </li>
        </ul>
        <div class="flex items-center justify-between text-xs text-muted-foreground">
          <span>{{ t('teamChat.nextcloud.connectedAs', { name: status.username }) }}</span>
          <button type="button" class="underline hover:text-foreground" @click="disconnect">{{ t('teamChat.nextcloud.disconnect') }}</button>
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>
