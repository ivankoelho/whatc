<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { contactsService, type Unit, type Department } from '@/services/api'
import { getErrorMessage } from '@/lib/api-utils'
import { CONTACT_TYPES, showsPlacement, placementPayload } from '@/lib/contact-registration'
import type { Contact } from '@/stores/contacts'

// The registration rows of a contact (type and document; unit and department only for a
// colaborador). It sits under the name and phone in the header of the panel. The pencil, the Save
// and Cancel buttons and `editing` belong to the parent: one edit covers the name too.
const props = defineProps<{ contact: Contact; editing: boolean; units: Unit[]; departments: Department[] }>()
const emit = defineEmits<{
  submit: []
  updated: [registration: Pick<Contact, 'contact_type' | 'cpf_cnpj' | 'unit_id' | 'department_id'>]
}>()

const { t } = useI18n()

const NONE = 'none' // the Select cannot hold an empty value
const form = ref({ contact_type: 'cliente' as string, cpf_cnpj: '', unit_id: NONE, department_id: NONE })

const type = computed(() => props.contact.contact_type || 'cliente')
const unitName = computed(() => props.units.find(u => u.id === props.contact.unit_id)?.name)
const departmentName = computed(() => props.departments.find(d => d.id === props.contact.department_id)?.name)

// The form starts from the contact every time it opens.
watch(() => props.editing, (editing) => {
  if (!editing) return
  form.value = {
    contact_type: type.value,
    cpf_cnpj: props.contact.cpf_cnpj || '',
    unit_id: props.contact.unit_id || NONE,
    department_id: props.contact.department_id || NONE,
  }
})

// What the form would change. Unit/department only count for a colaborador.
function hasChanges(): boolean {
  const f = form.value
  return f.contact_type !== type.value
    || f.cpf_cnpj !== (props.contact.cpf_cnpj || '')
    || (showsPlacement(f.contact_type) && (f.unit_id !== (props.contact.unit_id || NONE) || f.department_id !== (props.contact.department_id || NONE)))
}

// Saves the form; the parent closes the edit when everything it saved went through.
async function save(): Promise<boolean> {
  try {
    const payload: Record<string, any> = {
      contact_type: form.value.contact_type,
      ...placementPayload(
        form.value.contact_type,
        form.value.unit_id === NONE ? '' : form.value.unit_id,
        form.value.department_id === NONE ? '' : form.value.department_id,
      ),
    }
    // Only send the document when it was edited: with number masking on, the value shown is
    // masked and must not be written back.
    if (form.value.cpf_cnpj !== (props.contact.cpf_cnpj || '')) {
      payload.cpf_cnpj = form.value.cpf_cnpj
    }
    const res = await contactsService.update(props.contact.id, payload)
    const saved = (res.data as any).data || res.data
    emit('updated', {
      contact_type: saved.contact_type,
      cpf_cnpj: saved.cpf_cnpj,
      unit_id: saved.unit_id,
      department_id: saved.department_id,
    })
    toast.success(t('contacts.registrationSaved'))
    return true
  } catch (e) {
    toast.error(getErrorMessage(e, t('common.failedSave', { resource: t('resources.contact') })))
    return false
  }
}

defineExpose({ save, hasChanges })
</script>

<template>
  <div class="mt-3" data-testid="contact-registration">
    <!-- Read: labels in one column, values aligned in the next -->
    <dl v-if="!editing" class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
      <dt class="text-muted-foreground">{{ $t('contacts.type') }}</dt>
      <dd class="min-w-0 break-words" data-testid="contact-registration-type">{{ $t('contacts.types.' + type) }}</dd>

      <!-- no document: nothing to show (the edit form still has the field) -->
      <template v-if="contact.cpf_cnpj">
        <dt class="text-muted-foreground">{{ $t('contacts.document') }}</dt>
        <dd class="min-w-0 break-words" data-testid="contact-registration-document">{{ contact.cpf_cnpj }}</dd>
      </template>

      <template v-if="showsPlacement(type)">
        <dt class="text-muted-foreground">{{ $t('contacts.unit') }}</dt>
        <dd class="min-w-0 break-words" data-testid="contact-registration-unit">{{ unitName || (contact.unit_id ? $t('contacts.defined') : $t('contacts.notInformed')) }}</dd>

        <dt class="text-muted-foreground">{{ $t('contacts.department') }}</dt>
        <dd class="min-w-0 break-words" data-testid="contact-registration-department">{{ departmentName || (contact.department_id ? $t('contacts.defined') : $t('contacts.notInformed')) }}</dd>
      </template>
    </dl>

    <!-- Edit -->
    <form v-else class="space-y-3" @submit.prevent="emit('submit')">
      <div class="space-y-1.5">
        <Label class="text-xs">{{ $t('contacts.type') }}</Label>
        <Select v-model="form.contact_type">
          <SelectTrigger data-testid="contact-registration-type-select"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem v-for="ct in CONTACT_TYPES" :key="ct" :value="ct">{{ $t('contacts.types.' + ct) }}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div class="space-y-1.5">
        <Label class="text-xs">{{ $t('contacts.document') }}</Label>
        <Input v-model="form.cpf_cnpj" :placeholder="$t('contacts.documentPlaceholder')" data-testid="contact-registration-document-input" />
      </div>

      <template v-if="showsPlacement(form.contact_type)">
        <div class="space-y-1.5">
          <Label class="text-xs">{{ $t('contacts.unit') }}</Label>
          <Select v-model="form.unit_id">
            <SelectTrigger data-testid="contact-registration-unit-select"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem :value="NONE">{{ $t('contacts.noneSelected') }}</SelectItem>
              <SelectItem v-for="u in units" :key="u.id" :value="u.id">{{ u.name }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="space-y-1.5">
          <Label class="text-xs">{{ $t('contacts.department') }}</Label>
          <Select v-model="form.department_id">
            <SelectTrigger data-testid="contact-registration-department-select"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem :value="NONE">{{ $t('contacts.noneSelected') }}</SelectItem>
              <SelectItem v-for="d in departments" :key="d.id" :value="d.id">{{ d.name }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </template>

    </form>
  </div>
</template>
