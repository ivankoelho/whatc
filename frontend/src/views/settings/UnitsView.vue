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
import { unitsService, type Unit } from '@/services/api'
import { useCrudState } from '@/composables/useCrudState'
import { toast } from 'vue-sonner'
import { Plus, Building2, Pencil, Trash2 } from 'lucide-vue-next'
import { getErrorMessage } from '@/lib/api-utils'

const { t } = useI18n()

interface UnitFormData {
  name: string
  code: string
  type: string
  active: boolean
}

const defaultFormData: UnitFormData = { name: '', code: '', type: '', active: true }

const {
  items: units, isLoading, isSubmitting, isDialogOpen, editingItem: editingUnit, deleteDialogOpen, itemToDelete: unitToDelete,
  formData, openCreateDialog, openEditDialog: baseOpenEditDialog, openDeleteDialog, closeDialog, closeDeleteDialog,
} = useCrudState<Unit, UnitFormData>(defaultFormData)

const isDeleting = computed(() => isSubmitting.value)
const error = computed(() => false)

const columns = computed<Column<Unit>[]>(() => [
  { key: 'name', label: t('units.columnName') },
  { key: 'code', label: t('units.columnCode') },
  { key: 'type', label: t('units.columnType') },
  { key: 'active', label: t('units.columnActive'), align: 'center' },
  { key: 'actions', label: t('common.actions'), align: 'right' },
])

function openEditDialog(unit: Unit) {
  baseOpenEditDialog(unit, (u) => ({
    name: u.name,
    code: u.code || '',
    type: u.type || '',
    active: u.active,
  }))
}

async function fetchUnits() {
  isLoading.value = true
  try {
    const res = await unitsService.list()
    units.value = res.data.data.units
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('resources.units') })))
  } finally {
    isLoading.value = false
  }
}

onMounted(() => fetchUnits())

async function saveUnit() {
  if (!formData.value.name.trim()) {
    toast.error(t('units.nameRequired'))
    return
  }
  isSubmitting.value = true
  try {
    const payload = { ...formData.value, name: formData.value.name.trim() }
    if (editingUnit.value) {
      await unitsService.update(editingUnit.value.id, payload)
      toast.success(t('common.updatedSuccess', { resource: t('resources.Unit') }))
    } else {
      await unitsService.create(payload)
      toast.success(t('common.createdSuccess', { resource: t('resources.Unit') }))
    }
    closeDialog()
    await fetchUnits()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedSave', { resource: t('resources.unit') })))
  } finally {
    isSubmitting.value = false
  }
}

async function confirmDelete() {
  if (!unitToDelete.value) return
  isSubmitting.value = true
  try {
    await unitsService.delete(unitToDelete.value.id)
    toast.success(t('common.deletedSuccess', { resource: t('resources.Unit') }))
    closeDeleteDialog()
    await fetchUnits()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedDelete', { resource: t('resources.unit') })))
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('units.title')" :description="$t('units.subtitle')" :icon="Building2" icon-gradient="bg-gradient-to-br from-blue-500 to-cyan-600 shadow-blue-500/20" back-link="/settings">
      <template #actions>
        <Button variant="outline" size="sm" @click="openCreateDialog"><Plus class="h-4 w-4 mr-2" />{{ $t('units.addUnit') }}</Button>
      </template>
    </PageHeader>

    <ErrorState
      v-if="error && !isLoading"
      :title="$t('common.loadErrorTitle')"
      :description="$t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="fetchUnits"
    />

    <ScrollArea v-else orientation="vertical" class="flex-1">
      <div class="p-6">
        <div class="max-w-4xl mx-auto">
          <Card>
            <CardHeader>
              <CardTitle>{{ $t('units.cardTitle') }}</CardTitle>
              <CardDescription>{{ $t('units.cardDesc') }}</CardDescription>
            </CardHeader>
            <CardContent>
              <DataTable
                :items="units"
                :columns="columns"
                :is-loading="isLoading"
                :empty-icon="Building2"
                :empty-title="$t('units.noUnitsYet')"
                :empty-description="$t('units.noUnitsYetDesc')"
                item-name="units"
              >
                <template #cell-name="{ item: unit }">
                  <span class="font-medium">{{ unit.name }}</span>
                </template>
                <template #cell-code="{ item: unit }">
                  <span class="text-muted-foreground">{{ unit.code || '—' }}</span>
                </template>
                <template #cell-type="{ item: unit }">
                  <span class="text-muted-foreground">{{ unit.type || '—' }}</span>
                </template>
                <template #cell-active="{ item: unit }">
                  <Badge :variant="unit.active ? 'success' : 'secondary'">{{ unit.active ? $t('common.yes') : $t('common.no') }}</Badge>
                </template>
                <template #cell-actions="{ item: unit }">
                  <div class="flex items-center justify-end gap-1">
                    <IconButton :icon="Pencil" :label="$t('units.editUnit')" class="h-8 w-8" @click="openEditDialog(unit)" />
                    <IconButton :label="$t('units.deleteTitle')" class="h-8 w-8" @click="openDeleteDialog(unit)">
                      <Trash2 class="h-4 w-4 text-destructive" />
                    </IconButton>
                  </div>
                </template>
                <template #empty-action>
                  <Button variant="outline" size="sm" @click="openCreateDialog">
                    <Plus class="h-4 w-4 mr-2" />
                    {{ $t('units.addUnit') }}
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
      :is-editing="!!editingUnit"
      :is-submitting="isSubmitting"
      :edit-title="$t('units.editTitle')"
      :create-title="$t('units.createTitle')"
      :edit-description="$t('units.editDesc')"
      :create-description="$t('units.createDesc')"
      max-width="max-w-md"
      @submit="saveUnit"
    >
      <div class="space-y-4">
        <div class="space-y-2">
          <Label>{{ $t('units.name') }} <span class="text-destructive">*</span></Label>
          <Input v-model="formData.name" :placeholder="$t('units.namePlaceholder')" maxlength="255" />
        </div>
        <div class="space-y-2">
          <Label>{{ $t('units.code') }}</Label>
          <Input v-model="formData.code" :placeholder="$t('units.codePlaceholder')" maxlength="50" />
        </div>
        <div class="space-y-2">
          <Label>{{ $t('units.type') }}</Label>
          <Input v-model="formData.type" :placeholder="$t('units.typePlaceholder')" maxlength="50" />
        </div>
        <div class="flex items-center justify-between gap-4 pt-2">
          <Label>{{ $t('units.active') }}</Label>
          <Switch :checked="formData.active" @update:checked="formData.active = $event" />
        </div>
      </div>
    </CrudFormDialog>

    <DeleteConfirmDialog v-model:open="deleteDialogOpen" :title="$t('units.deleteTitle')" :item-name="unitToDelete?.name" :is-submitting="isDeleting" @confirm="confirmDelete">
      <p class="text-sm text-muted-foreground">{{ $t('units.deleteWarning') }}</p>
    </DeleteConfirmDialog>
  </div>
</template>
