<script setup lang="ts">
import { onMounted, computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Badge } from '@/components/ui/badge'
import { PageHeader, DataTable, CrudFormDialog, DeleteConfirmDialog, IconButton, ErrorState, type Column } from '@/components/shared'
import { departmentsService, type Department } from '@/services/api'
import { useCrudState } from '@/composables/useCrudState'
import { useAuthStore } from '@/stores/auth'
import { toast } from 'vue-sonner'
import { Plus, Network, Pencil, Trash2 } from 'lucide-vue-next'
import { getErrorMessage } from '@/lib/api-utils'

const { t } = useI18n()
const authStore = useAuthStore()

// The buttons only mirror departments:write / departments:delete; the API is the authority and
// answers 403 to anyone without them.
const canWrite = computed(() => authStore.hasPermission('departments', 'write'))
const canDelete = computed(() => authStore.hasPermission('departments', 'delete'))

interface DepartmentFormData {
  name: string
  active: boolean
}

const defaultFormData: DepartmentFormData = { name: '', active: true }

const {
  items: departments, isLoading, isSubmitting, isDialogOpen, editingItem: editingDepartment, deleteDialogOpen, itemToDelete: departmentToDelete,
  formData, openCreateDialog, openEditDialog: baseOpenEditDialog, openDeleteDialog, closeDialog, closeDeleteDialog,
} = useCrudState<Department, DepartmentFormData>(defaultFormData)

const loadError = ref<'forbidden' | 'failed' | null>(null)

const columns = computed<Column<Department>[]>(() => [
  { key: 'name', label: t('departments.columnName') },
  { key: 'active', label: t('departments.columnActive'), align: 'center' },
  ...(canWrite.value || canDelete.value ? [{ key: 'actions', label: t('common.actions'), align: 'right' } as Column<Department>] : []),
])

function openEditDialog(department: Department) {
  baseOpenEditDialog(department, (d) => ({ name: d.name, active: d.active }))
}

async function fetchDepartments() {
  isLoading.value = true
  loadError.value = null
  try {
    const res = await departmentsService.list()
    departments.value = res.data.data.departments
  } catch (e: any) {
    loadError.value = e?.response?.status === 403 ? 'forbidden' : 'failed'
    if (loadError.value === 'failed') {
      toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('resources.departments') })))
    }
  } finally {
    isLoading.value = false
  }
}

onMounted(() => fetchDepartments())

async function saveDepartment() {
  if (!formData.value.name.trim()) {
    toast.error(t('departments.nameRequired'))
    return
  }
  isSubmitting.value = true
  try {
    const payload = { name: formData.value.name.trim(), active: formData.value.active }
    if (editingDepartment.value) {
      await departmentsService.update(editingDepartment.value.id, payload)
      toast.success(t('common.updatedSuccess', { resource: t('resources.Department') }))
    } else {
      await departmentsService.create(payload)
      toast.success(t('common.createdSuccess', { resource: t('resources.Department') }))
    }
    closeDialog()
    await fetchDepartments()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedSave', { resource: t('resources.department') })))
  } finally {
    isSubmitting.value = false
  }
}

async function confirmDelete() {
  if (!departmentToDelete.value) return
  isSubmitting.value = true
  try {
    await departmentsService.delete(departmentToDelete.value.id)
    toast.success(t('common.deletedSuccess', { resource: t('resources.Department') }))
    closeDeleteDialog()
    await fetchDepartments()
  } catch (e) {
    // A department still in use answers 409 with its reason; shown as is.
    toast.error(getErrorMessage(e, t('common.failedDelete', { resource: t('resources.department') })))
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('departments.title')" :description="$t('departments.subtitle')" :icon="Network" icon-gradient="bg-gradient-to-br from-violet-500 to-purple-600 shadow-violet-500/20" back-link="/settings">
      <template #actions>
        <Button v-if="canWrite" variant="outline" size="sm" data-testid="department-add" @click="openCreateDialog"><Plus class="h-4 w-4 mr-2" />{{ $t('departments.addDepartment') }}</Button>
      </template>
    </PageHeader>

    <ErrorState
      v-if="loadError && !isLoading"
      :title="loadError === 'forbidden' ? $t('departments.loadForbidden') : $t('common.loadErrorTitle')"
      :description="loadError === 'forbidden' ? '' : $t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="fetchDepartments"
    />

    <ScrollArea v-else orientation="vertical" class="flex-1">
      <div class="p-6">
        <div class="max-w-4xl mx-auto">
          <Card>
            <CardHeader>
              <CardTitle>{{ $t('departments.cardTitle') }}</CardTitle>
              <CardDescription>{{ $t('departments.cardDesc') }}</CardDescription>
            </CardHeader>
            <CardContent>
              <DataTable
                :items="departments"
                :columns="columns"
                :is-loading="isLoading"
                :empty-icon="Network"
                :empty-title="$t('departments.noDepartmentsYet')"
                :empty-description="$t('departments.noDepartmentsYetDesc')"
                item-name="departments"
              >
                <template #cell-name="{ item: department }">
                  <span class="font-medium">{{ department.name }}</span>
                </template>
                <template #cell-active="{ item: department }">
                  <Badge :variant="department.active ? 'success' : 'secondary'">{{ department.active ? $t('common.yes') : $t('common.no') }}</Badge>
                </template>
                <template #cell-actions="{ item: department }">
                  <div class="flex items-center justify-end gap-1">
                    <IconButton v-if="canWrite" :icon="Pencil" :label="$t('departments.editDepartment')" class="h-8 w-8" @click="openEditDialog(department)" />
                    <IconButton v-if="canDelete" :label="$t('departments.deleteTitle')" class="h-8 w-8" @click="openDeleteDialog(department)">
                      <Trash2 class="h-4 w-4 text-destructive" />
                    </IconButton>
                  </div>
                </template>
                <template v-if="canWrite" #empty-action>
                  <Button variant="outline" size="sm" @click="openCreateDialog">
                    <Plus class="h-4 w-4 mr-2" />
                    {{ $t('departments.addDepartment') }}
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
      :is-editing="!!editingDepartment"
      :is-submitting="isSubmitting"
      :edit-title="$t('departments.editTitle')"
      :create-title="$t('departments.createTitle')"
      :edit-description="$t('departments.editDesc')"
      :create-description="$t('departments.createDesc')"
      max-width="max-w-md"
      @submit="saveDepartment"
    >
      <div class="space-y-4">
        <div class="space-y-2">
          <Label>{{ $t('departments.name') }} <span class="text-destructive">*</span></Label>
          <Input v-model="formData.name" :placeholder="$t('departments.namePlaceholder')" maxlength="255" data-testid="department-name" />
        </div>
        <div class="flex items-center justify-between gap-4 pt-2">
          <Label>{{ $t('departments.active') }}</Label>
          <Switch :checked="formData.active" @update:checked="formData.active = $event" />
        </div>
      </div>
    </CrudFormDialog>

    <DeleteConfirmDialog v-model:open="deleteDialogOpen" :title="$t('departments.deleteTitle')" :item-name="departmentToDelete?.name" :is-submitting="isSubmitting" @confirm="confirmDelete">
      <p class="text-sm text-muted-foreground">{{ $t('departments.deleteWarning') }}</p>
    </DeleteConfirmDialog>
  </div>
</template>
