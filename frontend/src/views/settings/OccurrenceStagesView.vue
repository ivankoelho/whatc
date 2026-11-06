<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Badge } from '@/components/ui/badge'
import draggable from 'vuedraggable'
import { PageHeader, CrudFormDialog, DeleteConfirmDialog, IconButton, ErrorState } from '@/components/shared'
import { occurrencesService, teamsService, type OccurrenceStage } from '@/services/api'
import { useOccurrencesStore } from '@/stores/occurrences'
import { useCrudState } from '@/composables/useCrudState'
import { toast } from 'vue-sonner'
import { Plus, ClipboardList, Pencil, Trash2, GripVertical, ChevronUp, ChevronDown, Loader2 } from 'lucide-vue-next'
import { getErrorMessage } from '@/lib/api-utils'

const { t } = useI18n()
const store = useOccurrencesStore()

interface StageFormData {
  name: string
  color: string
  position: number
  is_initial: boolean
  is_closing: boolean
  general_state: string
  team_id: string
}

const defaultFormData: StageFormData = { name: '', color: '#6b7280', position: 0, is_initial: false, is_closing: false, general_state: 'in_progress', team_id: '' }

const isLoading = ref(false)
const isDeleting = ref(false)
const error = ref(false)

// Ordering. The list is the whole pipeline; a move is sent as the full new order together with the order the
// screen started from, so the server can refuse it (409) when someone else reordered meanwhile. The screen then
// reloads the current order instead of guessing.
const isReordering = ref(false)
const announce = ref('') // polite live region for the keyboard/button alternative
const items = ref<OccurrenceStage[]>([])
watch(() => store.stages, (s) => { items.value = [...s] }, { immediate: true, deep: true })

async function persistOrder(next: OccurrenceStage[], movedName?: string) {
  const expected = store.stages.map(s => s.id)
  const ids = next.map(s => s.id)
  if (ids.join() === expected.join()) return
  isReordering.value = true
  try {
    await occurrencesService.reorderStages(ids, expected)
    store.stages = next.map((s, i) => ({ ...s, position: i }))
    if (movedName) announce.value = t('occurrences.stageMoved', { name: movedName, pos: ids.indexOf(next.find(s => s.name === movedName)!.id) + 1, total: ids.length })
  } catch (e: any) {
    items.value = [...store.stages]
    toast.error(e?.response?.status === 409 ? t('occurrences.stageOrderConflict') : getErrorMessage(e, t('occurrences.stageOrderFailed')))
    await fetchStages()
  } finally {
    isReordering.value = false
  }
}

function onDragEnd() { persistOrder([...items.value]) }

// the alternative to dragging: Move up / Move down
function move(index: number, delta: -1 | 1) {
  const j = index + delta
  if (j < 0 || j >= items.value.length) return
  const next = [...items.value]
  const [m] = next.splice(index, 1)
  next.splice(j, 0, m)
  items.value = next
  persistOrder(next, m.name)
}

const {
  isSubmitting, isDialogOpen, editingItem: editingStage, deleteDialogOpen, itemToDelete: stageToDelete,
  formData, openCreateDialog: baseOpenCreateDialog, openEditDialog: baseOpenEditDialog, openDeleteDialog, closeDialog, closeDeleteDialog,
} = useCrudState<OccurrenceStage, StageFormData>(defaultFormData)

function openCreateDialog() {
  baseOpenCreateDialog()
  formData.value.position = store.stages.length
}

function openEditDialog(stage: OccurrenceStage) {
  baseOpenEditDialog(stage, (s) => ({
    name: s.name,
    color: s.color || '#6b7280',
    position: s.position,
    is_initial: s.is_initial,
    is_closing: s.is_closing,
    general_state: s.general_state || (s.is_closing ? 'resolved' : s.is_initial ? 'new' : 'in_progress'),
    team_id: s.team_id || '',
  }))
}

async function fetchStages() {
  isLoading.value = true
  error.value = false
  try {
    await store.fetchStages()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('resources.stages') })))
    error.value = true
  } finally {
    isLoading.value = false
  }
}

const teams = ref<{ id: string; name: string }[]>([])
async function fetchTeams() {
  try {
    const res = await teamsService.list({ limit: 500 })
    teams.value = ((res.data as any).data || res.data).teams || []
  } catch { /* the team picker just stays empty without access */ }
}
onMounted(() => { fetchStages(); fetchTeams() })

async function saveStage() {
  if (!formData.value.name.trim()) {
    toast.error(t('occurrences.stageNameRequired'))
    return
  }
  isSubmitting.value = true
  try {
    const payload = { ...formData.value, name: formData.value.name.trim() }
    if (editingStage.value) {
      await occurrencesService.updateStage(editingStage.value.id, payload)
      toast.success(t('common.updatedSuccess', { resource: t('resources.Stage') }))
    } else {
      await occurrencesService.createStage(payload)
      toast.success(t('common.createdSuccess', { resource: t('resources.Stage') }))
    }
    closeDialog()
    await fetchStages()
  } catch (e) {
    // 409s here carry the backend's exact integrity-rule message (duplicate
    // name, or — on edit — trying to unmark the only initial/closing stage).
    toast.error(getErrorMessage(e, t('common.failedSave', { resource: t('resources.stage') })))
  } finally {
    isSubmitting.value = false
  }
}

async function confirmDelete() {
  if (!stageToDelete.value) return
  isDeleting.value = true
  try {
    await occurrencesService.deleteStage(stageToDelete.value.id)
    toast.success(t('common.deletedSuccess', { resource: t('resources.Stage') }))
    closeDeleteDialog()
    await fetchStages()
  } catch (e) {
    // 409s here explain exactly why: in use by occurrences, the initial
    // stage, or the last closing stage.
    toast.error(getErrorMessage(e, t('common.failedDelete', { resource: t('resources.stage') })))
  } finally {
    isDeleting.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('occurrences.stagesTitle')" :description="$t('occurrences.stagesSubtitle')" :icon="ClipboardList" icon-gradient="bg-gradient-to-br from-violet-500 to-purple-600 shadow-violet-500/20" back-link="/settings">
      <template #actions>
        <Button variant="outline" size="sm" @click="openCreateDialog"><Plus class="h-4 w-4 mr-2" />{{ $t('occurrences.addStage') }}</Button>
      </template>
    </PageHeader>

    <ErrorState
      v-if="error && !isLoading"
      :title="$t('common.loadErrorTitle')"
      :description="$t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="fetchStages"
    />

    <ScrollArea v-else orientation="vertical" class="flex-1">
      <div class="p-6">
        <div class="max-w-4xl mx-auto">
          <Card>
            <CardHeader>
              <CardTitle>{{ $t('occurrences.stagesCardTitle') }}</CardTitle>
              <CardDescription>{{ $t('occurrences.stagesCardDesc') }}</CardDescription>
            </CardHeader>
            <CardContent>
              <p class="sr-only" role="status" aria-live="polite" data-testid="stage-announce">{{ announce }}</p>
              <p v-if="!isLoading && items.length === 0" class="py-8 text-center text-sm text-muted-foreground">{{ $t('occurrences.noStagesYet') }}</p>
              <div v-else-if="isLoading" class="flex justify-center py-8"><Loader2 class="h-5 w-5 animate-spin text-muted-foreground" aria-hidden="true" /></div>
              <draggable
                v-else v-model="items" item-key="id" handle=".stage-handle" tag="ul" :animation="150" :disabled="isReordering"
                class="divide-y rounded-md border" data-testid="stage-list" @end="onDragEnd"
              >
                <template #item="{ element: stage, index }">
                  <li class="flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2.5" :data-testid="'stage-row-' + stage.id">
                    <button type="button" class="stage-handle flex h-9 w-9 shrink-0 cursor-grab touch-none items-center justify-center rounded text-muted-foreground hover:bg-muted active:cursor-grabbing" :aria-label="$t('occurrences.stageDrag', { name: stage.name })" :title="$t('occurrences.stageDrag', { name: stage.name })">
                      <GripVertical class="h-4 w-4" aria-hidden="true" />
                    </button>
                    <span class="inline-block h-4 w-4 shrink-0 rounded-full border border-white/20 light:border-gray-300" :style="{ backgroundColor: stage.color }" />
                    <span class="min-w-0 flex-1 basis-40 font-medium [overflow-wrap:anywhere]">{{ stage.name }}</span>
                    <div class="flex flex-wrap items-center gap-1.5">
                      <Badge variant="secondary">{{ $t('occurrences.generalStates.' + (stage.general_state || (stage.is_closing ? 'resolved' : stage.is_initial ? 'new' : 'in_progress'))) }}</Badge>
                      <Badge v-if="stage.is_initial" variant="success">{{ $t('occurrences.columnInitial') }}</Badge>
                      <Badge v-if="stage.is_closing" variant="info">{{ $t('occurrences.columnClosing') }}</Badge>
                    </div>
                    <div class="ml-auto flex items-center gap-0.5">
                      <IconButton :icon="ChevronUp" :label="$t('occurrences.stageMoveUp')" class="h-9 w-9" :disabled="isReordering || index === 0" :data-testid="'stage-up-' + stage.id" @click="move(index, -1)" />
                      <IconButton :icon="ChevronDown" :label="$t('occurrences.stageMoveDown')" class="h-9 w-9" :disabled="isReordering || index === items.length - 1" :data-testid="'stage-down-' + stage.id" @click="move(index, 1)" />
                      <IconButton :icon="Pencil" :label="$t('occurrences.editStage')" class="h-9 w-9" @click="openEditDialog(stage)" />
                      <IconButton :label="$t('occurrences.deleteStageTitle')" class="h-9 w-9" @click="openDeleteDialog(stage)">
                        <Trash2 class="h-4 w-4 text-destructive" />
                      </IconButton>
                    </div>
                  </li>
                </template>
              </draggable>
            </CardContent>
          </Card>
        </div>
      </div>
    </ScrollArea>

    <CrudFormDialog
      v-model:open="isDialogOpen"
      :is-editing="!!editingStage"
      :is-submitting="isSubmitting"
      :edit-title="$t('occurrences.editStageTitle')"
      :create-title="$t('occurrences.createStageTitle')"
      :edit-description="$t('occurrences.editStageDesc')"
      :create-description="$t('occurrences.createStageDesc')"
      max-width="max-w-md"
      @submit="saveStage"
    >
      <div class="space-y-4">
        <div class="space-y-2">
          <Label>{{ $t('occurrences.stageName') }} <span class="text-destructive">*</span></Label>
          <Input v-model="formData.name" :placeholder="$t('occurrences.stageNamePlaceholder')" maxlength="100" />
        </div>
        <div class="space-y-2">
          <Label>{{ $t('occurrences.stageColor') }}</Label>
          <div class="flex items-center gap-2">
            <Input type="color" v-model="formData.color" class="h-10 w-16 p-1 cursor-pointer" />
            <Input v-model="formData.color" class="flex-1 font-mono" maxlength="20" />
          </div>
        </div>
        <div class="space-y-2">
          <Label>{{ $t('occurrences.generalState') }}</Label>
          <select v-model="formData.general_state" class="flex h-10 w-full rounded-md border border-input bg-background px-3 text-sm" data-testid="stage-general-state" @change="formData.is_closing = formData.general_state === 'resolved'">
            <option v-for="s in ['new', 'in_progress', 'waiting_customer', 'resolved']" :key="s" :value="s">{{ $t('occurrences.generalStates.' + s) }}</option>
          </select>
          <p class="text-xs text-muted-foreground">{{ $t('occurrences.generalStateHint') }}</p>
        </div>
        <div class="space-y-2">
          <Label>{{ $t('occurrences.stageTeam') }}</Label>
          <select v-model="formData.team_id" :disabled="!!editingStage" class="flex h-10 w-full rounded-md border border-input bg-background px-3 text-sm disabled:opacity-60" data-testid="stage-team">
            <option value="">{{ $t('occurrences.stageTeamAll') }}</option>
            <option v-for="tm in teams" :key="tm.id" :value="tm.id">{{ tm.name }}</option>
          </select>
          <p class="text-xs text-muted-foreground">{{ $t('occurrences.stageTeamHint') }}</p>
        </div>
        <div class="space-y-2">
          <Label>{{ $t('occurrences.stagePosition') }}</Label>
          <Input type="number" v-model.number="formData.position" :min="0" />
        </div>
        <div class="flex items-center justify-between gap-4 pt-2">
          <div>
            <Label>{{ $t('occurrences.isInitial') }}</Label>
            <p class="text-xs text-muted-foreground">{{ $t('occurrences.isInitialHint') }}</p>
          </div>
          <Switch :checked="formData.is_initial" @update:checked="formData.is_initial = $event" />
        </div>
        <div class="flex items-center justify-between gap-4">
          <div>
            <Label>{{ $t('occurrences.isClosing') }}</Label>
            <p class="text-xs text-muted-foreground">{{ $t('occurrences.isClosingHint') }}</p>
          </div>
          <Switch :checked="formData.is_closing" @update:checked="formData.is_closing = $event" />
        </div>
      </div>
    </CrudFormDialog>

    <DeleteConfirmDialog v-model:open="deleteDialogOpen" :title="$t('occurrences.deleteStageTitle')" :item-name="stageToDelete?.name" :is-submitting="isDeleting" @confirm="confirmDelete">
      <p class="text-sm text-muted-foreground">{{ $t('occurrences.deleteStageWarning') }}</p>
    </DeleteConfirmDialog>
  </div>
</template>
