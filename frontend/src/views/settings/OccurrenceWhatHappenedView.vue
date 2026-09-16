<script setup lang="ts">
import { onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Badge } from '@/components/ui/badge'
import { PageHeader, DataTable, CrudFormDialog, DeleteConfirmDialog, IconButton, ErrorState, type Column } from '@/components/shared'
import { occurrenceWhatHappenedService, type OccurrenceWhatHappened } from '@/services/api'
import { useCrudState } from '@/composables/useCrudState'
import { toast } from 'vue-sonner'
import { Plus, HelpCircle, Pencil, Trash2 } from 'lucide-vue-next'
import { getErrorMessage } from '@/lib/api-utils'

const { t } = useI18n()

interface ReasonFormData {
  name: string
  position: number
  is_active: boolean
}

const defaultFormData: ReasonFormData = { name: '', position: 0, is_active: true }

const {
  items: reasons, isLoading, isSubmitting, isDialogOpen, editingItem: editingReason, deleteDialogOpen, itemToDelete: reasonToDelete,
  formData, openCreateDialog: baseOpenCreateDialog, openEditDialog: baseOpenEditDialog, openDeleteDialog, closeDialog, closeDeleteDialog,
} = useCrudState<OccurrenceWhatHappened, ReasonFormData>(defaultFormData)

const error = computed(() => false)

const columns = computed<Column<OccurrenceWhatHappened>[]>(() => [
  { key: 'name', label: t('whatHappened.columnName') },
  { key: 'position', label: t('whatHappened.columnPosition'), width: 'w-[100px]' },
  { key: 'is_active', label: t('whatHappened.columnActive'), align: 'center' },
  { key: 'actions', label: t('common.actions'), align: 'right' },
])

function openCreateDialog() {
  baseOpenCreateDialog()
  formData.value.position = reasons.value.length
}

function openEditDialog(reason: OccurrenceWhatHappened) {
  baseOpenEditDialog(reason, (r) => ({ name: r.name, position: r.position, is_active: r.is_active }))
}

async function fetchReasons() {
  isLoading.value = true
  try {
    const res = await occurrenceWhatHappenedService.list()
    reasons.value = res.data.data.reasons
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('whatHappened.title') })))
  } finally {
    isLoading.value = false
  }
}

onMounted(fetchReasons)

async function saveReason() {
  if (!formData.value.name.trim()) {
    toast.error(t('whatHappened.nameRequired'))
    return
  }
  isSubmitting.value = true
  try {
    const payload = { ...formData.value, name: formData.value.name.trim() }
    if (editingReason.value) {
      await occurrenceWhatHappenedService.update(editingReason.value.id, payload)
      toast.success(t('common.updatedSuccess', { resource: t('whatHappened.reason') }))
    } else {
      await occurrenceWhatHappenedService.create(payload)
      toast.success(t('common.createdSuccess', { resource: t('whatHappened.reason') }))
    }
    closeDialog()
    await fetchReasons()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedSave', { resource: t('whatHappened.reason') })))
  } finally {
    isSubmitting.value = false
  }
}

async function confirmDelete() {
  if (!reasonToDelete.value) return
  isSubmitting.value = true
  try {
    await occurrenceWhatHappenedService.delete(reasonToDelete.value.id)
    toast.success(t('common.deletedSuccess', { resource: t('whatHappened.reason') }))
    closeDeleteDialog()
    await fetchReasons()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedDelete', { resource: t('whatHappened.reason') })))
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('whatHappened.title')" :description="$t('whatHappened.subtitle')" :icon="HelpCircle" icon-gradient="bg-gradient-to-br from-rose-500 to-red-600 shadow-rose-500/20" back-link="/settings">
      <template #actions>
        <Button variant="outline" size="sm" @click="openCreateDialog"><Plus class="h-4 w-4 mr-2" />{{ $t('whatHappened.addReason') }}</Button>
      </template>
    </PageHeader>

    <ErrorState
      v-if="error && !isLoading"
      :title="$t('common.loadErrorTitle')"
      :description="$t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="fetchReasons"
    />

    <ScrollArea v-else orientation="vertical" class="flex-1">
      <div class="p-6">
        <div class="max-w-4xl mx-auto">
          <Card>
            <CardHeader>
              <CardTitle>{{ $t('whatHappened.cardTitle') }}</CardTitle>
              <CardDescription>{{ $t('whatHappened.cardDesc') }}</CardDescription>
            </CardHeader>
            <CardContent>
              <DataTable
                :items="reasons"
                :columns="columns"
                :is-loading="isLoading"
                :empty-icon="HelpCircle"
                :empty-title="$t('whatHappened.noReasonsYet')"
                :empty-description="$t('whatHappened.noReasonsYetDesc')"
                item-name="reasons"
              >
                <template #cell-name="{ item: reason }">
                  <span class="font-medium">{{ reason.name }}</span>
                </template>
                <template #cell-position="{ item: reason }">
                  <span class="text-muted-foreground">{{ reason.position }}</span>
                </template>
                <template #cell-is_active="{ item: reason }">
                  <Badge :variant="reason.is_active ? 'success' : 'secondary'">{{ reason.is_active ? $t('common.yes') : $t('common.no') }}</Badge>
                </template>
                <template #cell-actions="{ item: reason }">
                  <div class="flex items-center justify-end gap-1">
                    <IconButton :icon="Pencil" :label="$t('whatHappened.editReason')" class="h-8 w-8" @click="openEditDialog(reason)" />
                    <IconButton :label="$t('whatHappened.deleteTitle')" class="h-8 w-8" @click="openDeleteDialog(reason)">
                      <Trash2 class="h-4 w-4 text-destructive" />
                    </IconButton>
                  </div>
                </template>
                <template #empty-action>
                  <Button variant="outline" size="sm" @click="openCreateDialog">
                    <Plus class="h-4 w-4 mr-2" />
                    {{ $t('whatHappened.addReason') }}
                  </Button>
                </template>
              </DataTable>
            </CardContent>
          </Card>
        </div>
      </div>
    </ScrollArea>

    <CrudFormDialog
      v-model:open="isDialogOpen"
      :is-editing="!!editingReason"
      :is-submitting="isSubmitting"
      :edit-title="$t('whatHappened.editTitle')"
      :create-title="$t('whatHappened.createTitle')"
      :edit-description="$t('whatHappened.editDesc')"
      :create-description="$t('whatHappened.createDesc')"
      max-width="max-w-md"
      @submit="saveReason"
    >
      <div class="space-y-4">
        <div class="space-y-2">
          <Label>{{ $t('whatHappened.name') }} <span class="text-destructive">*</span></Label>
          <Input v-model="formData.name" :placeholder="$t('whatHappened.namePlaceholder')" maxlength="100" />
        </div>
        <div class="space-y-2">
          <Label>{{ $t('whatHappened.position') }}</Label>
          <Input type="number" v-model.number="formData.position" :min="0" />
        </div>
        <div class="flex items-center justify-between gap-4 pt-2">
          <Label>{{ $t('whatHappened.active') }}</Label>
          <Switch :checked="formData.is_active" @update:checked="formData.is_active = $event" />
        </div>
      </div>
    </CrudFormDialog>

    <DeleteConfirmDialog v-model:open="deleteDialogOpen" :title="$t('whatHappened.deleteTitle')" :item-name="reasonToDelete?.name" :is-submitting="isSubmitting" @confirm="confirmDelete">
      <p class="text-sm text-muted-foreground">{{ $t('whatHappened.deleteWarning') }}</p>
    </DeleteConfirmDialog>
  </div>
</template>
