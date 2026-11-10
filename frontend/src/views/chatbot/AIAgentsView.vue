<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Switch } from '@/components/ui/switch'
import { Checkbox } from '@/components/ui/checkbox'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { PageHeader, CrudFormDialog, DeleteConfirmDialog, IconButton, ErrorState } from '@/components/shared'
import { chatbotService, accountsService } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { toast } from 'vue-sonner'
import { Plus, Pencil, Trash2, Bot, AlertTriangle } from 'lucide-vue-next'

const { t } = useI18n()

interface AIAgent {
  id: string
  name: string
  objective: string
  instructions: string
  company_info: string
  tone: string
  handoff_rules: string
  is_enabled: boolean
  whatsapp_accounts: string[]
}
interface Issue { account: string; problem: string }

const NONE = '__none__' // a Select item cannot carry an empty value

const agents = ref<AIAgent[]>([])
const issues = ref<Record<string, Issue[]>>({})
const accounts = ref<{ name: string }[]>([])
const isLoading = ref(true)
const loadError = ref<string | null>(null)

// organization-wide settings that belong to agents: the default agent and the monthly cap
const defaultAgentId = ref<string>(NONE)
const monthlyLimit = ref<number>(0)
const isSavingSettings = ref(false)

// create / edit dialog
const dialogOpen = ref(false)
const editing = ref<AIAgent | null>(null)
const isSubmitting = ref(false)
const emptyForm = () => ({
  name: '', objective: '', instructions: '', company_info: '', tone: '', handoff_rules: '',
  is_enabled: false, whatsapp_accounts: [] as string[],
})
const form = ref(emptyForm())

// delete dialog
const deleteOpen = ref(false)
const toDelete = ref<AIAgent | null>(null)
const isDeleting = ref(false)

const unwrap = (res: any) => res?.data?.data ?? res?.data ?? {}

async function loadIssues(list: AIAgent[]) {
  const out: Record<string, Issue[]> = {}
  await Promise.all(list.filter(a => a.whatsapp_accounts?.length).map(async a => {
    try {
      out[a.id] = unwrap(await chatbotService.getAIAgentReadiness(a.id)).issues ?? []
    } catch {
      out[a.id] = []
    }
  }))
  issues.value = out
}

async function load() {
  isLoading.value = true
  loadError.value = null
  try {
    const [list, settings, accs] = await Promise.all([
      chatbotService.listAIAgents({ limit: 100 }),
      chatbotService.getSettings(),
      accountsService.list(),
    ])
    agents.value = unwrap(list).agents ?? []
    const s = unwrap(settings).settings ?? {}
    defaultAgentId.value = s.ai_default_agent_id || NONE
    monthlyLimit.value = s.ai_monthly_token_limit ?? 0
    accounts.value = unwrap(accs).accounts ?? []
    await loadIssues(agents.value)
  } catch (err) {
    console.error('Failed to load AI agents:', err)
    loadError.value = t('aiAgents.fetchError')
  } finally {
    isLoading.value = false
  }
}

onMounted(load)

const enabledAgents = computed(() => agents.value.filter(a => a.is_enabled))

async function saveSettings() {
  isSavingSettings.value = true
  try {
    await chatbotService.updateSettings({
      ai_default_agent_id: defaultAgentId.value === NONE ? '' : defaultAgentId.value,
      ai_monthly_token_limit: Math.max(0, Math.floor(Number(monthlyLimit.value) || 0)),
    })
    toast.success(t('aiAgents.settingsSaved'))
  } catch (err) {
    toast.error(getErrorMessage(err, t('aiAgents.settingsFailed')))
  } finally {
    isSavingSettings.value = false
  }
}

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  dialogOpen.value = true
}

function openEdit(agent: AIAgent) {
  editing.value = agent
  form.value = {
    name: agent.name, objective: agent.objective ?? '', instructions: agent.instructions ?? '',
    company_info: agent.company_info ?? '', tone: agent.tone ?? '', handoff_rules: agent.handoff_rules ?? '',
    is_enabled: agent.is_enabled, whatsapp_accounts: [...(agent.whatsapp_accounts ?? [])],
  }
  dialogOpen.value = true
}

function toggleAccount(name: string, on: boolean) {
  const set = new Set(form.value.whatsapp_accounts)
  if (on) set.add(name)
  else set.delete(name)
  form.value.whatsapp_accounts = [...set]
}

async function save() {
  if (!form.value.name.trim()) {
    toast.error(t('aiAgents.nameRequired'))
    return
  }
  isSubmitting.value = true
  try {
    if (editing.value) await chatbotService.updateAIAgent(editing.value.id, form.value)
    else await chatbotService.createAIAgent(form.value)
    toast.success(t('aiAgents.saved'))
    dialogOpen.value = false
    await load()
  } catch (err) {
    toast.error(getErrorMessage(err, t('aiAgents.saveFailed')))
  } finally {
    isSubmitting.value = false
  }
}

async function toggleEnabled(agent: AIAgent) {
  try {
    await chatbotService.updateAIAgent(agent.id, { is_enabled: !agent.is_enabled })
    agent.is_enabled = !agent.is_enabled
  } catch (err) {
    toast.error(getErrorMessage(err, t('aiAgents.saveFailed')))
  }
}

function askDelete(agent: AIAgent) {
  toDelete.value = agent
  deleteOpen.value = true
}

async function confirmDelete() {
  if (!toDelete.value) return
  isDeleting.value = true
  try {
    await chatbotService.deleteAIAgent(toDelete.value.id)
    toast.success(t('aiAgents.deleted'))
    deleteOpen.value = false
    await load()
  } catch (err) {
    toast.error(getErrorMessage(err, t('aiAgents.deleteFailed')))
  } finally {
    isDeleting.value = false
  }
}

const issueLabel = (i: Issue) => `${i.account}: ${t('aiAgents.issues.' + i.problem)}`
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50" data-testid="ai-agents-view">
    <PageHeader
      :title="$t('aiAgents.title')"
      :icon="Bot"
      icon-gradient="bg-gradient-to-br from-orange-500 to-amber-600 shadow-orange-500/20"
      back-link="/chatbot"
      :breadcrumbs="[{ label: $t('aiAgents.backToChatbot'), href: '/chatbot' }, { label: $t('aiAgents.title') }]"
    >
      <template #actions>
        <Button variant="outline" size="sm" data-testid="new-agent" @click="openCreate">
          <Plus class="h-4 w-4 mr-2" />
          {{ $t('aiAgents.add') }}
        </Button>
      </template>
    </PageHeader>

    <ScrollArea class="flex-1">
      <div class="p-6 space-y-6">
        <ErrorState v-if="loadError" :title="$t('common.loadErrorTitle')" :description="loadError" :retry-label="$t('common.retry')" @retry="load" />

        <template v-else>
          <Card>
            <CardHeader>
              <CardTitle>{{ $t('aiAgents.settingsTitle') }}</CardTitle>
              <CardDescription>{{ $t('aiAgents.settingsDesc') }}</CardDescription>
            </CardHeader>
            <CardContent class="space-y-4">
              <div class="grid gap-4 md:grid-cols-2">
                <div class="space-y-2">
                  <Label>{{ $t('aiAgents.defaultAgent') }}</Label>
                  <Select v-model="defaultAgentId">
                    <SelectTrigger data-testid="default-agent"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem :value="NONE">{{ $t('aiAgents.noDefault') }}</SelectItem>
                      <SelectItem v-for="a in enabledAgents" :key="a.id" :value="a.id">{{ a.name }}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div class="space-y-2">
                  <Label>{{ $t('aiAgents.monthlyLimit') }}</Label>
                  <Input v-model.number="monthlyLimit" type="number" min="0" step="1000" data-testid="monthly-limit" />
                  <p class="text-xs text-muted-foreground">{{ $t('aiAgents.monthlyLimitHelp') }}</p>
                </div>
              </div>
              <Button size="sm" :disabled="isSavingSettings" data-testid="save-settings" @click="saveSettings">{{ $t('common.save') }}</Button>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{{ $t('aiAgents.yourAgents') }}</CardTitle>
              <CardDescription>{{ $t('aiAgents.yourAgentsDesc') }}</CardDescription>
            </CardHeader>
            <CardContent>
              <p v-if="!isLoading && !agents.length" class="text-sm text-muted-foreground" data-testid="no-agents">{{ $t('aiAgents.empty') }}</p>
              <ul v-else class="divide-y divide-white/10 light:divide-gray-200">
                <li v-for="agent in agents" :key="agent.id" class="py-3 flex items-start justify-between gap-4" :data-testid="`agent-row-${agent.id}`">
                  <div class="min-w-0 space-y-1">
                    <div class="flex items-center gap-2 flex-wrap">
                      <span class="font-medium">{{ agent.name }}</span>
                      <Badge v-if="agent.id === defaultAgentId" class="text-xs">{{ $t('aiAgents.defaultBadge') }}</Badge>
                      <Badge v-for="acc in agent.whatsapp_accounts" :key="acc" variant="secondary" class="text-xs">{{ acc }}</Badge>
                      <span v-if="!agent.whatsapp_accounts?.length" class="text-xs text-muted-foreground">{{ $t('aiAgents.noAccounts') }}</span>
                    </div>
                    <p v-if="agent.objective" class="text-sm text-muted-foreground truncate">{{ agent.objective }}</p>
                    <p v-for="i in issues[agent.id] ?? []" :key="i.account" class="text-xs text-amber-500 flex items-center gap-1" data-testid="agent-issue">
                      <AlertTriangle class="h-3 w-3" /> {{ issueLabel(i) }}
                    </p>
                  </div>
                  <div class="flex items-center gap-2 shrink-0">
                    <Switch :checked="agent.is_enabled" :data-testid="`toggle-${agent.id}`" @update:checked="toggleEnabled(agent)" />
                    <IconButton :icon="Pencil" :label="$t('aiAgents.edit')" class="h-8 w-8" @click="openEdit(agent)" />
                    <IconButton :icon="Trash2" :label="$t('aiAgents.delete')" class="h-8 w-8 text-destructive" @click="askDelete(agent)" />
                  </div>
                </li>
              </ul>
            </CardContent>
          </Card>
        </template>
      </div>
    </ScrollArea>

    <CrudFormDialog
      v-model:open="dialogOpen"
      :is-editing="!!editing"
      :is-submitting="isSubmitting"
      :edit-title="$t('aiAgents.editTitle')"
      :create-title="$t('aiAgents.createTitle')"
      :edit-description="$t('aiAgents.formDesc')"
      :create-description="$t('aiAgents.formDesc')"
      max-width="max-w-2xl"
      @submit="save"
    >
      <div class="space-y-4 max-h-[65vh] overflow-y-auto pr-1">
        <div class="space-y-2">
          <Label>{{ $t('aiAgents.name') }} <span class="text-destructive">*</span></Label>
          <Input v-model="form.name" maxlength="255" data-testid="field-name" />
        </div>
        <div class="space-y-2">
          <Label>{{ $t('aiAgents.objective') }}</Label>
          <Textarea v-model="form.objective" :rows="2" maxlength="4000" data-testid="field-objective" />
        </div>
        <div class="space-y-2">
          <Label>{{ $t('aiAgents.instructions') }}</Label>
          <Textarea v-model="form.instructions" :rows="5" maxlength="4000" data-testid="field-instructions" />
        </div>
        <div class="space-y-2">
          <Label>{{ $t('aiAgents.companyInfo') }}</Label>
          <Textarea v-model="form.company_info" :rows="4" maxlength="4000" data-testid="field-company-info" />
        </div>
        <div class="space-y-2">
          <Label>{{ $t('aiAgents.tone') }}</Label>
          <Textarea v-model="form.tone" :rows="2" maxlength="4000" data-testid="field-tone" />
        </div>
        <div class="space-y-2">
          <Label>{{ $t('aiAgents.handoffRules') }}</Label>
          <Textarea v-model="form.handoff_rules" :rows="3" maxlength="4000" data-testid="field-handoff" />
        </div>
        <div class="space-y-2">
          <Label>{{ $t('aiAgents.accounts') }}</Label>
          <p class="text-xs text-muted-foreground">{{ $t('aiAgents.accountsHelp') }}</p>
          <div v-for="acc in accounts" :key="acc.name" class="flex items-center gap-2">
            <Checkbox
              :checked="form.whatsapp_accounts.includes(acc.name)"
              :data-testid="`account-${acc.name}`"
              @update:checked="(v: boolean | 'indeterminate') => toggleAccount(acc.name, v === true)"
            />
            <span class="text-sm">{{ acc.name }}</span>
          </div>
          <p v-if="!form.whatsapp_accounts.length" class="text-xs text-amber-500" data-testid="no-accounts-warning">{{ $t('aiAgents.noAccountsWarning') }}</p>
        </div>
        <div class="flex items-center gap-2">
          <Switch :checked="form.is_enabled" data-testid="field-enabled" @update:checked="(v: boolean) => (form.is_enabled = v)" />
          <Label>{{ $t('aiAgents.enabled') }}</Label>
        </div>
      </div>
    </CrudFormDialog>

    <DeleteConfirmDialog v-model:open="deleteOpen" :title="$t('aiAgents.deleteTitle')" :item-name="toDelete?.name" :is-submitting="isDeleting" @confirm="confirmDelete">
      <p class="text-sm text-muted-foreground">{{ $t('aiAgents.deleteWarning') }}</p>
    </DeleteConfirmDialog>
  </div>
</template>
