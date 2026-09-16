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
import { occurrenceCategoriesService, type OccurrenceCategory } from '@/services/api'
import { useCrudState } from '@/composables/useCrudState'
import { toast } from 'vue-sonner'
import { Plus, Tag, Pencil, Trash2 } from 'lucide-vue-next'
import { getErrorMessage } from '@/lib/api-utils'

const { t } = useI18n()

// Sem seletor de "categoria pai" nesta versão: as seis categorias pedidas
// são todas de topo, e o backend (Fase 3) já suporta um nível de
// subcategoria se/quando isso for pedido — só falta a tela, que fica pra
// quando houver uma subcategoria real pra gerenciar.
interface CategoryFormData {
  name: string
  position: number
  is_active: boolean
}

const defaultFormData: CategoryFormData = { name: '', position: 0, is_active: true }

const {
  items: categories, isLoading, isSubmitting, isDialogOpen, editingItem: editingCategory, deleteDialogOpen, itemToDelete: categoryToDelete,
  formData, openCreateDialog: baseOpenCreateDialog, openEditDialog: baseOpenEditDialog, openDeleteDialog, closeDialog, closeDeleteDialog,
} = useCrudState<OccurrenceCategory, CategoryFormData>(defaultFormData)

const error = computed(() => false)

// Categorias de topo só, para a lista desta tela (parent_id ausente).
const topLevelCategories = computed(() => categories.value.filter(c => !c.parent_id))

const columns = computed<Column<OccurrenceCategory>[]>(() => [
  { key: 'name', label: t('categories.columnName') },
  { key: 'position', label: t('categories.columnPosition'), width: 'w-[100px]' },
  { key: 'is_active', label: t('categories.columnActive'), align: 'center' },
  { key: 'actions', label: t('common.actions'), align: 'right' },
])

function openCreateDialog() {
  baseOpenCreateDialog()
  formData.value.position = topLevelCategories.value.length
}

function openEditDialog(category: OccurrenceCategory) {
  baseOpenEditDialog(category, (c) => ({ name: c.name, position: c.position, is_active: c.is_active }))
}

async function fetchCategories() {
  isLoading.value = true
  try {
    const res = await occurrenceCategoriesService.list()
    categories.value = res.data.data.categories
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedLoad', { resource: t('categories.title') })))
  } finally {
    isLoading.value = false
  }
}

onMounted(fetchCategories)

async function saveCategory() {
  if (!formData.value.name.trim()) {
    toast.error(t('categories.nameRequired'))
    return
  }
  isSubmitting.value = true
  try {
    const payload = { ...formData.value, name: formData.value.name.trim() }
    if (editingCategory.value) {
      await occurrenceCategoriesService.update(editingCategory.value.id, payload)
      toast.success(t('common.updatedSuccess', { resource: t('categories.category') }))
    } else {
      await occurrenceCategoriesService.create(payload)
      toast.success(t('common.createdSuccess', { resource: t('categories.category') }))
    }
    closeDialog()
    await fetchCategories()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedSave', { resource: t('categories.category') })))
  } finally {
    isSubmitting.value = false
  }
}

async function confirmDelete() {
  if (!categoryToDelete.value) return
  isSubmitting.value = true
  try {
    await occurrenceCategoriesService.delete(categoryToDelete.value.id)
    toast.success(t('common.deletedSuccess', { resource: t('categories.category') }))
    closeDeleteDialog()
    await fetchCategories()
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedDelete', { resource: t('categories.category') })))
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <PageHeader :title="$t('categories.title')" :description="$t('categories.subtitle')" :icon="Tag" icon-gradient="bg-gradient-to-br from-emerald-500 to-teal-600 shadow-emerald-500/20" back-link="/settings">
      <template #actions>
        <Button variant="outline" size="sm" @click="openCreateDialog"><Plus class="h-4 w-4 mr-2" />{{ $t('categories.addCategory') }}</Button>
      </template>
    </PageHeader>

    <ErrorState
      v-if="error && !isLoading"
      :title="$t('common.loadErrorTitle')"
      :description="$t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="fetchCategories"
    />

    <ScrollArea v-else orientation="vertical" class="flex-1">
      <div class="p-6">
        <div class="max-w-4xl mx-auto">
          <Card>
            <CardHeader>
              <CardTitle>{{ $t('categories.cardTitle') }}</CardTitle>
              <CardDescription>{{ $t('categories.cardDesc') }}</CardDescription>
            </CardHeader>
            <CardContent>
              <DataTable
                :items="topLevelCategories"
                :columns="columns"
                :is-loading="isLoading"
                :empty-icon="Tag"
                :empty-title="$t('categories.noCategoriesYet')"
                :empty-description="$t('categories.noCategoriesYetDesc')"
                item-name="categories"
              >
                <template #cell-name="{ item: category }">
                  <span class="font-medium">{{ category.name }}</span>
                </template>
                <template #cell-position="{ item: category }">
                  <span class="text-muted-foreground">{{ category.position }}</span>
                </template>
                <template #cell-is_active="{ item: category }">
                  <Badge :variant="category.is_active ? 'success' : 'secondary'">{{ category.is_active ? $t('common.yes') : $t('common.no') }}</Badge>
                </template>
                <template #cell-actions="{ item: category }">
                  <div class="flex items-center justify-end gap-1">
                    <IconButton :icon="Pencil" :label="$t('categories.editCategory')" class="h-8 w-8" @click="openEditDialog(category)" />
                    <IconButton :label="$t('categories.deleteTitle')" class="h-8 w-8" @click="openDeleteDialog(category)">
                      <Trash2 class="h-4 w-4 text-destructive" />
                    </IconButton>
                  </div>
                </template>
                <template #empty-action>
                  <Button variant="outline" size="sm" @click="openCreateDialog">
                    <Plus class="h-4 w-4 mr-2" />
                    {{ $t('categories.addCategory') }}
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
      :is-editing="!!editingCategory"
      :is-submitting="isSubmitting"
      :edit-title="$t('categories.editTitle')"
      :create-title="$t('categories.createTitle')"
      :edit-description="$t('categories.editDesc')"
      :create-description="$t('categories.createDesc')"
      max-width="max-w-md"
      @submit="saveCategory"
    >
      <div class="space-y-4">
        <div class="space-y-2">
          <Label>{{ $t('categories.name') }} <span class="text-destructive">*</span></Label>
          <Input v-model="formData.name" :placeholder="$t('categories.namePlaceholder')" maxlength="100" />
        </div>
        <div class="space-y-2">
          <Label>{{ $t('categories.position') }}</Label>
          <Input type="number" v-model.number="formData.position" :min="0" />
        </div>
        <div class="flex items-center justify-between gap-4 pt-2">
          <Label>{{ $t('categories.active') }}</Label>
          <Switch :checked="formData.is_active" @update:checked="formData.is_active = $event" />
        </div>
      </div>
    </CrudFormDialog>

    <DeleteConfirmDialog v-model:open="deleteDialogOpen" :title="$t('categories.deleteTitle')" :item-name="categoryToDelete?.name" :is-submitting="isSubmitting" @confirm="confirmDelete">
      <p class="text-sm text-muted-foreground">{{ $t('categories.deleteWarning') }}</p>
    </DeleteConfirmDialog>
  </div>
</template>
