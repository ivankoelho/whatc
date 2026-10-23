<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { TagBadge } from '@/components/ui/tag-badge'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from '@/components/ui/command'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { contactsService, accountsService, type Tag } from '@/services/api'
import { useTagsStore } from '@/stores/tags'
import { toast } from 'vue-sonner'
import { Loader2, Check, ChevronsUpDown, X, AlertTriangle } from 'lucide-vue-next'
import { getErrorMessage, isConflictError } from '@/lib/api-utils'
import { getTagColorClass } from '@/lib/constants'

const { t, te } = useI18n()
const tagsStore = useTagsStore()
const router = useRouter()

interface Props {
  open: boolean
}

const props = defineProps<Props>()
const emit = defineEmits<{
  'update:open': [value: boolean]
  'created': [contact: any]
}>()

interface ContactFormData {
  phone_number: string
  profile_name: string
  whatsapp_account: string
  tags: string[]
}

const defaultFormData: ContactFormData = { phone_number: '', profile_name: '', whatsapp_account: '', tags: [] }

const formData = ref<ContactFormData>({ ...defaultFormData })
const isSubmitting = ref(false)
// Set when the number already exists: either someone owns it (nothing to do
// here, the agent needs to ask them) or it sits unassigned in the main queue
// and the agent can confirm taking it.
// `blockedReason` is a stable backend code (see contacts.claimBlocked.*) set when
// the agent could not take the contact: the dialog then explains why instead of
// offering an action that would be refused.
const duplicate = ref<{
  code: string
  contactId: string
  contactName: string
  ownerName: string
  assignedToMe: boolean
  deleted: boolean
  blockedReason: string
} | null>(null)

function claimBlockedText(reason: string): string {
  const key = `contacts.claimBlocked.${reason}`
  return te(key) ? t(key) : t('contacts.claimBlocked.unknown')
}
const tagSelectorOpen = ref(false)
const availableTags = ref<Tag[]>([])
const availableAccounts = ref<{ id: string; name: string; phone_number: string }[]>([])

function applyDuplicate(info: any) {
  duplicate.value = {
    code: info.code,
    contactId: info.contact_id || '',
    contactName: info.contact_name || '',
    ownerName: info.assigned_user_name || '',
    assignedToMe: !!info.assigned_to_me,
    deleted: !!info.deleted,
    blockedReason: info.can_claim === false ? (info.claim_blocked_reason || 'unknown') : ''
  }
}

// A different number is a different question: forget the previous answer, then
// ask the backend (debounced) whether the new one is already registered, in any
// spelling, so the agent is offered the conversation before filling the form.
let lookupTimer: ReturnType<typeof setTimeout> | undefined
watch(() => formData.value.phone_number, (phone) => {
  duplicate.value = null
  clearTimeout(lookupTimer)
  if (phone.replace(/\D/g, '').length < 8) return
  lookupTimer = setTimeout(async () => {
    try {
      const res = await contactsService.lookup(phone.trim())
      const info = res.data?.data || res.data
      // Drop a stale answer: the field changed while the request was in flight.
      if (info?.found && formData.value.phone_number === phone) applyDuplicate(info)
    } catch {
      // Best effort: the create call still enforces uniqueness.
    }
  }, 400)
})

function openConversation() {
  if (!duplicate.value?.contactId) return
  const id = duplicate.value.contactId
  emit('update:open', false)
  router.push(`/chat/${id}`)
}

watch(() => props.open, (isOpen) => {
  if (isOpen) {
    formData.value = { ...defaultFormData }
    duplicate.value = null
    fetchTags()
    fetchAccounts()
  }
})

async function fetchTags() {
  try {
    const response = await tagsStore.fetchTags({ limit: 100 })
    availableTags.value = response.tags
  } catch {
    // Silently fail - tags are optional
  }
}

async function fetchAccounts() {
  try {
    const response = await accountsService.list()
    const data = response.data as any
    const responseData = data.data || data
    availableAccounts.value = responseData.accounts || []
  } catch (error) {
    // Silently fail - accounts are optional
  }
}

async function saveContact(claim = false) {
  if (!formData.value.phone_number.trim()) {
    toast.error(t('contacts.phoneRequired'))
    return
  }
  isSubmitting.value = true
  // `duplicate` stays up while a claim is retried, so a refusal (403) leaves the
  // agent looking at why they were asked, not at a form that lost its context.
  try {
    const response = await contactsService.create({
      phone_number: formData.value.phone_number.trim(),
      profile_name: formData.value.profile_name.trim() || undefined,
      whatsapp_account: formData.value.whatsapp_account || undefined,
      tags: formData.value.tags.length > 0 ? formData.value.tags : undefined,
      claim: claim || undefined
    })
    const contact = response.data?.data || response.data
    toast.success(claim ? t('contacts.claimedSuccess') : t('common.createdSuccess', { resource: t('resources.Contact') }))
    emit('update:open', false)
    emit('created', contact)
  } catch (error: any) {
    const info = error?.response?.data?.data
    if (isConflictError(error) && (info?.code === 'contact_exists_assigned' || info?.code === 'contact_exists_unassigned')) {
      applyDuplicate(info)
      return
    }
    // The claim was refused (403) with a stable code: show the reason in the
    // dialog and drop the action, rather than a raw backend sentence.
    if (error?.response?.status === 403 && claim && info?.code && duplicate.value) {
      duplicate.value = { ...duplicate.value, blockedReason: info.code }
      return
    }
    toast.error(getErrorMessage(error, t('common.failedCreate', { resource: t('resources.contact') })))
  } finally {
    isSubmitting.value = false
  }
}

function toggleTag(tagName: string) {
  const index = formData.value.tags.indexOf(tagName)
  if (index === -1) {
    formData.value.tags.push(tagName)
  } else {
    formData.value.tags.splice(index, 1)
  }
}

function removeTag(tagName: string) {
  formData.value.tags = formData.value.tags.filter(t => t !== tagName)
}

function isTagSelected(tagName: string): boolean {
  return formData.value.tags.includes(tagName)
}

function getTagDetails(tagName: string): Tag | undefined {
  return availableTags.value.find(t => t.name === tagName)
}

function closeDialog() {
  emit('update:open', false)
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{{ $t('contacts.createTitle') }}</DialogTitle>
        <DialogDescription>{{ $t('contacts.createDesc') }}</DialogDescription>
      </DialogHeader>
      <div class="space-y-4 py-4">
        <div class="space-y-2">
          <Label>{{ $t('contacts.phoneNumber') }} <span class="text-destructive">*</span></Label>
          <Input v-model="formData.phone_number" data-testid="create-contact-phone" :placeholder="$t('contacts.phonePlaceholder')" />
          <p class="text-xs text-muted-foreground">{{ $t('contacts.phoneHint') }}</p>
        </div>
        <div class="space-y-2">
          <Label>{{ $t('contacts.profileName') }}</Label>
          <Input v-model="formData.profile_name" :placeholder="$t('contacts.namePlaceholder')" />
        </div>
        <div v-if="availableAccounts.length > 0" class="space-y-2">
          <Label>{{ $t('contacts.whatsappAccount') }}</Label>
          <Select v-model="formData.whatsapp_account">
            <SelectTrigger>
              <SelectValue :placeholder="$t('contacts.selectAccount')" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="account in availableAccounts" :key="account.id" :value="account.name">
                {{ account.name }} ({{ account.phone_number }})
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div v-if="availableTags.length > 0" class="space-y-2">
          <Label>{{ $t('contacts.tags') }}</Label>
          <Popover v-model:open="tagSelectorOpen">
            <PopoverTrigger as-child>
              <Button variant="outline" role="combobox" class="w-full justify-between">
                <span v-if="formData.tags.length === 0" class="text-muted-foreground">{{ $t('contacts.selectTags') }}</span>
                <span v-else>{{ formData.tags.length }} {{ $t('contacts.tagsSelected') }}</span>
                <ChevronsUpDown class="ml-2 h-4 w-4 shrink-0 opacity-50" />
              </Button>
            </PopoverTrigger>
            <PopoverContent class="w-[300px] p-0" @interact-outside="(e) => e.preventDefault()">
              <Command>
                <CommandInput :placeholder="$t('contacts.searchTags')" />
                <CommandList>
                  <CommandEmpty>{{ $t('contacts.noTagsFound') }}</CommandEmpty>
                  <CommandGroup>
                    <CommandItem
                      v-for="tag in availableTags"
                      :key="tag.name"
                      :value="tag.name"
                      class="flex items-center gap-2 cursor-pointer"
                      @select.prevent="toggleTag(tag.name)"
                    >
                      <div class="flex items-center gap-2 flex-1">
                        <span :class="['w-2 h-2 rounded-full', getTagColorClass(tag.color).split(' ')[0]]"></span>
                        <span>{{ tag.name }}</span>
                      </div>
                      <Check v-if="isTagSelected(tag.name)" class="h-4 w-4 text-primary" />
                    </CommandItem>
                  </CommandGroup>
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>
          <div v-if="formData.tags.length > 0" class="flex flex-wrap gap-1 mt-2">
            <TagBadge
              v-for="tagName in formData.tags"
              :key="tagName"
              :color="getTagDetails(tagName)?.color"
            >
              {{ tagName }}
              <button
                type="button"
                class="ml-1 rounded-full hover:bg-black/10 dark:hover:bg-white/10 p-0.5 transition-colors"
                @click.stop="removeTag(tagName)"
              >
                <X class="h-3 w-3" />
              </button>
            </TagBadge>
          </div>
        </div>
      </div>
      <div v-if="duplicate" class="flex gap-2 rounded-md border border-amber-500/50 bg-amber-500/10 p-3 text-sm" role="alert">
        <AlertTriangle class="h-4 w-4 mt-0.5 shrink-0 text-amber-600" />
        <div class="space-y-1">
          <p class="font-medium">{{ $t('contacts.duplicateTitle') }}</p>
          <template v-if="duplicate.code === 'contact_exists_unassigned'">
            <p v-if="duplicate.blockedReason">
              {{ $t('contacts.duplicateBlocked', { name: duplicate.contactName || formData.phone_number }) }}
              {{ claimBlockedText(duplicate.blockedReason) }}
            </p>
            <p v-else-if="duplicate.deleted">
              {{ $t('contacts.duplicateDeleted', { name: duplicate.contactName || formData.phone_number }) }}
            </p>
            <p v-else>
              {{ $t('contacts.duplicateUnassigned', { name: duplicate.contactName || formData.phone_number }) }}
            </p>
          </template>
          <p v-else-if="duplicate.assignedToMe">{{ $t('contacts.duplicateMine') }}</p>
          <p v-else>
            {{ $t('contacts.duplicateAssigned', { name: duplicate.contactName || formData.phone_number, owner: duplicate.ownerName || $t('contacts.duplicateUnknownOwner') }) }}
          </p>
        </div>
      </div>
      <div class="flex justify-end gap-2">
        <Button variant="outline" @click="closeDialog">{{ $t('common.cancel') }}</Button>
        <Button v-if="duplicate?.assignedToMe && duplicate.contactId && !duplicate.deleted" data-testid="create-contact-open" @click="openConversation">
          {{ $t('contacts.openConversation') }}
        </Button>
        <Button v-else-if="duplicate?.code === 'contact_exists_unassigned' && !duplicate.blockedReason" data-testid="create-contact-claim" @click="saveContact(true)" :disabled="isSubmitting">
          <Loader2 v-if="isSubmitting" class="h-4 w-4 mr-2 animate-spin" />
          {{ $t('contacts.duplicateClaim') }}
        </Button>
        <!-- Once the number is known to exist and cannot be taken, repeating the same request only repeats the answer. -->
        <Button v-else data-testid="create-contact-submit" @click="saveContact()" :disabled="isSubmitting || !!duplicate">
          <Loader2 v-if="isSubmitting" class="h-4 w-4 mr-2 animate-spin" />
          {{ $t('common.create') }}
        </Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
