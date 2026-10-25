<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Badge } from '@/components/ui/badge'
import { PageHeader, DataTable, CrudFormDialog, DeleteConfirmDialog, IconButton, ErrorState, type Column } from '@/components/shared'
import {
  occurrenceAutomationsService, occurrencesService, accountsService, templatesService, unitsService,
  type OccurrenceAutomation,
} from '@/services/api'
import { useAuthStore } from '@/stores/auth'
import { getErrorMessage } from '@/lib/api-utils'
import { formatDateTime } from '@/lib/utils'
import { Plus, Pencil, Trash2, Send } from 'lucide-vue-next'

const { t } = useI18n()
const auth = useAuthStore()
const canWrite = computed(() => auth.hasPermission('accounts', 'write'))

const items = ref<OccurrenceAutomation[]>([])
const sendingEnabled = ref(true)
const isLoading = ref(false)
const error = ref(false)
const isDialogOpen = ref(false)
const isSubmitting = ref(false)
const editing = ref<OccurrenceAutomation | null>(null)
const toDelete = ref<OccurrenceAutomation | null>(null)
const deleteOpen = ref(false)

const accounts = ref<{ name: string }[]>([])
const units = ref<{ id: string; name: string }[]>([])
const teams = ref<{ id: string; name: string; unit_id?: string }[]>([])
const templates = ref<{ name: string; language: string; category: string; status: string }[]>([])

const emptyForm = () => ({
  unit_id: '', team_id: '', whatsapp_account: '', template_name: '', template_language: 'pt_BR',
  fallback_template_name: '', is_active: true, max_attempts: 3, retry_backoff_secs: 60, on_failure: 'manual_retry' as 'manual_retry' | 'cancel',
})
const form = ref(emptyForm())

// Only approved UTILITY templates of the chosen account can back an automation:
// the same rule the server enforces.
const usable = computed(() => templates.value.filter(x => x.status?.toUpperCase() === 'APPROVED' && x.category?.toUpperCase() === 'UTILITY'))
const languages = computed(() => [...new Set(usable.value.map(x => x.language))])
const templateNames = computed(() => [...new Set(usable.value.filter(x => !form.value.template_language || x.language === form.value.template_language).map(x => x.name))])
const teamOptions = computed(() => teams.value.filter(tm => !form.value.unit_id || tm.unit_id === form.value.unit_id))

const columns = computed<Column<OccurrenceAutomation>[]>(() => [
  { key: 'scope', label: t('occurrences.autoScope') },
  { key: 'account', label: t('occurrences.noticeAccount') },
  { key: 'template', label: t('occurrences.noticeTemplate') },
  { key: 'state', label: t('occurrences.autoState'), align: 'center' },
  { key: 'last', label: t('occurrences.autoLastRun') },
  { key: 'actions', label: t('common.actions'), align: 'right' },
])

function scopeLabel(a: OccurrenceAutomation) {
  if (a.team_name) return `${a.team_name}${a.unit_name ? ' — ' + a.unit_name : ''}`
  if (a.unit_name) return a.unit_name
  return t('occurrences.autoWholeOrg')
}

async function load() {
  isLoading.value = true
  error.value = false
  try {
    const res = await occurrenceAutomationsService.list()
    const b = res.data as any
    items.value = (b.data ?? b).configs ?? []
    sendingEnabled.value = (b.data ?? b).sending_enabled !== false
  } catch (e) {
    error.value = true
    toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('occurrences.automations') })))
  } finally {
    isLoading.value = false
  }
}

async function loadOptions() {
  try {
    const [acc, un, tg] = await Promise.all([accountsService.list(), unitsService.list(), occurrencesService.forwardTargets()])
    const a = acc.data as any
    accounts.value = (a.data ?? a).accounts ?? []
    const u = un.data as any
    units.value = (u.data ?? u).units ?? []
    const g = tg.data as any
    teams.value = (g.data ?? g).targets ?? []
  } catch { /* the pickers stay empty without access */ }
}

async function loadTemplates() {
  templates.value = []
  if (!form.value.whatsapp_account) return
  try {
    const res = await templatesService.list({ account: form.value.whatsapp_account, limit: 200 })
    const d = res.data as any
    templates.value = (d.data ?? d).templates ?? []
  } catch { templates.value = [] }
}
watch(() => form.value.whatsapp_account, loadTemplates)

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  isDialogOpen.value = true
}

function openEdit(a: OccurrenceAutomation) {
  editing.value = a
  form.value = {
    unit_id: a.unit_id || '', team_id: a.team_id || '', whatsapp_account: a.whatsapp_account, template_name: a.template_name || '',
    template_language: a.template_language || 'pt_BR', fallback_template_name: a.fallback_template_name || '',
    is_active: a.is_active, max_attempts: a.max_attempts, retry_backoff_secs: a.retry_backoff_secs, on_failure: a.on_failure,
  }
  isDialogOpen.value = true
}

async function save() {
  isSubmitting.value = true
  try {
    const f = form.value
    await occurrenceAutomationsService.save({
      unit_id: f.unit_id || undefined, team_id: f.team_id || undefined, whatsapp_account: f.whatsapp_account,
      template_name: f.template_name, template_language: f.template_language, fallback_template_name: f.fallback_template_name,
      is_active: f.is_active, max_attempts: Number(f.max_attempts), retry_backoff_secs: Number(f.retry_backoff_secs), on_failure: f.on_failure,
    })
    toast.success(t('occurrences.autoSaved'))
    isDialogOpen.value = false
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.autoSaveFailed')))
  } finally {
    isSubmitting.value = false
  }
}

async function toggle(a: OccurrenceAutomation) {
  if (!canWrite.value) return
  try {
    await occurrenceAutomationsService.save({
      unit_id: a.unit_id, team_id: a.team_id, whatsapp_account: a.whatsapp_account, template_name: a.template_name,
      template_language: a.template_language, fallback_template_name: a.fallback_template_name, is_active: !a.is_active,
    })
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.autoSaveFailed')))
  }
}

async function confirmDelete() {
  if (!toDelete.value) return
  try {
    await occurrenceAutomationsService.remove(toDelete.value.id)
    deleteOpen.value = false
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('occurrences.autoSaveFailed')))
  }
}

onMounted(() => { load(); loadOptions() })
</script>

<template>
  <div class="flex flex-col h-full">
    <PageHeader :title="$t('occurrences.automations')" :subtitle="$t('occurrences.automationsDesc')" :icon="Send" icon-gradient="bg-gradient-to-br from-emerald-500 to-teal-600 shadow-emerald-500/20">
      <template #actions>
        <Button v-if="canWrite" size="sm" data-testid="automation-add" @click="openCreate">
          <Plus class="h-4 w-4 mr-2" />{{ $t('occurrences.autoAdd') }}
        </Button>
      </template>
    </PageHeader>

    <ScrollArea class="flex-1">
      <div class="p-6">
        <Card>
          <CardHeader>
            <CardTitle>{{ $t('occurrences.automations') }}</CardTitle>
            <CardDescription>{{ $t('occurrences.autoHint') }}</CardDescription>
          </CardHeader>
          <CardContent>
            <p v-if="!sendingEnabled" class="mb-4 rounded-md border border-amber-500/50 bg-amber-500/10 px-3 py-2 text-sm" data-testid="sending-off">{{ $t('occurrences.autoSendingOff') }}</p>
            <ErrorState v-if="error" :title="$t('common.loadFailed')" @retry="load" />
            <DataTable v-else :items="items" :columns="columns" :is-loading="isLoading" :empty-icon="Send"
              :empty-title="$t('occurrences.autoNone')" :empty-description="$t('occurrences.autoNoneDesc')" item-name="automations">
              <template #cell-scope="{ item }">
                <span class="font-medium" data-testid="automation-scope">{{ scopeLabel(item) }}</span>
              </template>
              <template #cell-account="{ item }">{{ item.whatsapp_account }}</template>
              <template #cell-template="{ item }">
                <span v-if="item.template_name">{{ item.template_name }} <span class="text-muted-foreground">({{ item.template_language }})</span></span>
                <span v-else class="text-muted-foreground">—</span>
              </template>
              <template #cell-state="{ item }">
                <div class="flex items-center justify-center gap-2">
                  <Switch :checked="item.is_active" :disabled="!canWrite" :aria-label="$t('occurrences.autoState')" data-testid="automation-toggle" @update:checked="toggle(item)" />
                  <Badge :variant="item.is_active ? 'default' : 'secondary'">{{ item.is_active ? $t('occurrences.autoOn') : $t('occurrences.autoOff') }}</Badge>
                </div>
              </template>
              <template #cell-last="{ item }">
                <div class="text-xs">
                  <span v-if="item.last_run_at">{{ formatDateTime(item.last_run_at as string) }} · {{ $t('occurrences.notice.' + item.last_status) }}</span>
                  <span v-else class="text-muted-foreground">—</span>
                  <Badge v-if="item.recent_failures > 0" variant="destructive" class="ml-2">{{ $t('occurrences.autoFailures', { count: item.recent_failures }) }}</Badge>
                </div>
              </template>
              <template #cell-actions="{ item }">
                <div v-if="canWrite" class="flex items-center justify-end gap-1">
                  <IconButton :icon="Pencil" :label="$t('common.edit')" class="h-8 w-8" @click="openEdit(item)" />
                  <IconButton :label="$t('common.delete')" class="h-8 w-8" @click="toDelete = item; deleteOpen = true"><Trash2 class="h-4 w-4 text-destructive" /></IconButton>
                </div>
              </template>
            </DataTable>
          </CardContent>
        </Card>
      </div>
    </ScrollArea>

    <CrudFormDialog v-model:open="isDialogOpen" :is-editing="!!editing" :is-submitting="isSubmitting"
      :edit-title="$t('occurrences.autoEdit')" :create-title="$t('occurrences.autoAdd')"
      :edit-description="$t('occurrences.autoHint')" :create-description="$t('occurrences.autoHint')" max-width="max-w-lg" @submit="save">
      <div class="space-y-4">
        <div class="grid grid-cols-2 gap-3">
          <div class="space-y-2">
            <Label>{{ $t('occurrences.unitLabel') }}</Label>
            <select v-model="form.unit_id" :disabled="!!editing" class="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm" data-testid="auto-unit">
              <option value="">{{ $t('occurrences.autoWholeOrg') }}</option>
              <option v-for="u in units" :key="u.id" :value="u.id">{{ u.name }}</option>
            </select>
          </div>
          <div class="space-y-2">
            <Label>{{ $t('occurrences.stageTeam') }}</Label>
            <select v-model="form.team_id" :disabled="!!editing" class="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm" data-testid="auto-team">
              <option value="">{{ $t('occurrences.stageTeamAll') }}</option>
              <option v-for="tm in teamOptions" :key="tm.id" :value="tm.id">{{ tm.name }}</option>
            </select>
          </div>
        </div>
        <div class="space-y-2">
          <Label>{{ $t('occurrences.noticeAccount') }} *</Label>
          <select v-model="form.whatsapp_account" class="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm" data-testid="auto-account">
            <option value="" disabled>—</option>
            <option v-for="acc in accounts" :key="acc.name" :value="acc.name">{{ acc.name }}</option>
          </select>
        </div>
        <div class="grid grid-cols-3 gap-3">
          <div class="space-y-2 col-span-2">
            <Label>{{ $t('occurrences.noticeTemplate') }}</Label>
            <select v-model="form.template_name" class="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm" data-testid="auto-template">
              <option value="">—</option>
              <option v-for="n in templateNames" :key="n" :value="n">{{ n }}</option>
            </select>
          </div>
          <div class="space-y-2">
            <Label>{{ $t('occurrences.autoLanguage') }}</Label>
            <select v-model="form.template_language" class="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm">
              <option v-for="l in (languages.length ? languages : [form.template_language])" :key="l" :value="l">{{ l }}</option>
            </select>
          </div>
        </div>
        <div class="space-y-2">
          <Label>{{ $t('occurrences.autoFallback') }}</Label>
          <select v-model="form.fallback_template_name" class="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm">
            <option value="">—</option>
            <option v-for="n in templateNames" :key="n" :value="n">{{ n }}</option>
          </select>
          <p class="text-xs text-muted-foreground">{{ $t('occurrences.autoFallbackHint') }}</p>
        </div>
        <div class="grid grid-cols-3 gap-3">
          <div class="space-y-2"><Label>{{ $t('occurrences.autoAttempts') }}</Label><Input v-model.number="form.max_attempts" type="number" min="1" max="10" /></div>
          <div class="space-y-2"><Label>{{ $t('occurrences.autoBackoff') }}</Label><Input v-model.number="form.retry_backoff_secs" type="number" min="10" /></div>
          <div class="space-y-2">
            <Label>{{ $t('occurrences.autoOnFailure') }}</Label>
            <select v-model="form.on_failure" class="flex h-9 w-full rounded-md border border-input bg-background px-3 text-sm">
              <option value="manual_retry">{{ $t('occurrences.autoManualRetry') }}</option>
              <option value="cancel">{{ $t('occurrences.autoCancel') }}</option>
            </select>
          </div>
        </div>
        <div class="flex items-center justify-between gap-4">
          <Label>{{ $t('occurrences.autoActive') }}</Label>
          <Switch :checked="form.is_active" @update:checked="form.is_active = $event" />
        </div>
      </div>
    </CrudFormDialog>

    <DeleteConfirmDialog v-model:open="deleteOpen" :title="$t('occurrences.autoDelete')" :item-name="toDelete ? scopeLabel(toDelete) : ''" :is-submitting="false" @confirm="confirmDelete">
      <p class="text-sm text-muted-foreground">{{ $t('occurrences.autoDeleteWarning') }}</p>
    </DeleteConfirmDialog>
  </div>
</template>
