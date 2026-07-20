<script setup lang="ts">
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'

const open = defineModel<boolean>('open', { default: false })

const props = withDefaults(defineProps<{
  title?: string
  itemName?: string
  description?: string
  confirmLabel?: string
  cancelLabel?: string
  isSubmitting?: boolean
}>(), {
  isSubmitting: false,
})

const { t } = useI18n()

const titleText = computed(() => props.title || t('common.deleteItemTitle'))
const confirmText = computed(() => props.confirmLabel || t('common.delete'))
const cancelText = computed(() => props.cancelLabel || t('common.cancel'))

const emit = defineEmits<{
  confirm: []
  cancel: []
}>()

function handleConfirm() {
  emit('confirm')
}

function handleCancel() {
  open.value = false
  emit('cancel')
}
</script>

<template>
  <AlertDialog v-model:open="open">
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>{{ titleText }}</AlertDialogTitle>
        <AlertDialogDescription>
          <slot name="description">
            <template v-if="description">{{ description }}</template>
            <template v-else-if="itemName">
              {{ t('common.deleteNamed', { name: itemName }) }}
            </template>
            <template v-else>
              {{ t('common.deleteThisItem') }}
            </template>
          </slot>
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel :disabled="isSubmitting" @click="handleCancel">{{ cancelText }}</AlertDialogCancel>
        <Button
          variant="destructive"
          :loading="isSubmitting"
          @click="handleConfirm"
        >
          {{ confirmText }}
        </Button>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
