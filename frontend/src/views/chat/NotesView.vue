<script setup lang="ts">
import { ref, computed, watch, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useDebounceFn } from '@vueuse/core'
import { contactsService } from '@/services/api'
import type { Contact } from '@/stores/contacts'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import ConversationNotes from '@/components/chat/ConversationNotes.vue'
import { getInitials, getAvatarGradient } from '@/lib/utils'
import { Search, StickyNote, MessageSquare, ArrowLeft, Loader2, User } from 'lucide-vue-next'

// Internal notes, reachable without opening a conversation: pick a contact on
// the left, read/write its notes on the right. ?contact=<id> deep-links one
// (the chat's notes button and shared links use it).

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const search = ref('')
const contacts = ref<Contact[]>([])
const isLoading = ref(false)
const selected = ref<Contact | null>(null)

const selectedId = computed(() => (typeof route.query.contact === 'string' ? route.query.contact : null))

async function loadContacts() {
  isLoading.value = true
  try {
    const res = await contactsService.list({ search: search.value.trim() || undefined, limit: 50 })
    const data = res.data.data || res.data
    contacts.value = data.contacts || []
  } catch {
    contacts.value = []
  } finally {
    isLoading.value = false
  }
}

const debouncedLoad = useDebounceFn(loadContacts, 300)
watch(search, () => debouncedLoad())

// Resolve the deep-linked contact even when it isn't on the first page of results.
watch(selectedId, async (id) => {
  if (!id) { selected.value = null; return }
  if (selected.value?.id === id) return
  const fromList = contacts.value.find(c => c.id === id)
  if (fromList) { selected.value = fromList; return }
  try {
    const res = await contactsService.get(id)
    selected.value = res.data.data || res.data
  } catch {
    selected.value = null
  }
}, { immediate: true })

function pick(contact: Contact) {
  selected.value = contact
  router.replace({ query: { ...route.query, contact: contact.id } })
}

function clearSelection() {
  const query = { ...route.query }
  delete query.contact
  router.replace({ query })
}

function displayName(c: Contact) {
  return c.name || c.profile_name || c.phone_number
}

onMounted(loadContacts)
</script>

<template>
  <div class="flex h-full bg-[#0a0a0b] light:bg-gray-50">
    <!-- Contact picker (full screen on mobile until one is chosen) -->
    <div
      class="flex w-full shrink-0 flex-col border-r border-white/[0.08] bg-[#0a0a0b] light:border-gray-200 light:bg-white md:w-80"
      :class="selected && 'hidden md:flex'"
    >
      <div class="border-b border-white/[0.08] p-3 light:border-gray-200">
        <div class="relative">
          <Search class="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-white/40 light:text-gray-400" aria-hidden="true" />
          <Input
            v-model="search"
            :placeholder="t('chat.searchContacts') + '...'"
            :aria-label="t('chat.searchContacts')"
            class="h-9 pl-8 text-sm"
          />
        </div>
      </div>

      <ScrollArea class="scroll-clamp-x flex-1">
        <div v-if="isLoading && contacts.length === 0" class="flex justify-center p-6">
          <Loader2 class="h-5 w-5 animate-spin text-muted-foreground" />
        </div>
        <ul v-else-if="contacts.length" class="p-1.5" role="listbox" :aria-label="t('chat.notesPickContact')">
          <li v-for="c in contacts" :key="c.id">
            <button
              type="button"
              role="option"
              :aria-selected="selected?.id === c.id"
              class="flex w-full items-center gap-3 rounded-xl px-2.5 py-2 text-left transition-colors hover:bg-white/[0.06] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring light:hover:bg-gray-100"
              :class="selected?.id === c.id && 'bg-primary/15 hover:bg-primary/15 light:hover:bg-primary/15'"
              @click="pick(c)"
            >
              <Avatar class="h-9 w-9 shrink-0">
                <AvatarImage :src="c.avatar_url" />
                <AvatarFallback :class="'bg-gradient-to-br text-xs text-white ' + getAvatarGradient(displayName(c))">
                  {{ getInitials(displayName(c)) }}
                </AvatarFallback>
              </Avatar>
              <span class="min-w-0">
                <span class="block truncate text-sm font-medium text-white light:text-gray-900">{{ displayName(c) }}</span>
                <span class="block truncate text-xs text-white/50 light:text-gray-500">{{ c.phone_number }}</span>
              </span>
            </button>
          </li>
        </ul>
        <div v-else class="p-6 text-center text-sm text-muted-foreground">
          <User class="mx-auto mb-2 h-6 w-6 opacity-50" aria-hidden="true" />
          {{ t('chat.noContacts') }}
        </div>
      </ScrollArea>
    </div>

    <!-- Notes for the chosen contact -->
    <div v-if="selected" class="flex min-w-0 flex-1 flex-col">
      <div class="flex h-14 shrink-0 items-center gap-3 border-b border-white/[0.08] bg-[#0f0f10] px-4 light:border-gray-200 light:bg-white">
        <Button variant="ghost" size="icon" class="-ml-2 h-8 w-8 md:hidden" :aria-label="t('common.back')" @click="clearSelection">
          <ArrowLeft class="h-4 w-4" />
        </Button>
        <Avatar class="h-8 w-8">
          <AvatarImage :src="selected.avatar_url" />
          <AvatarFallback :class="'bg-gradient-to-br text-xs text-white ' + getAvatarGradient(displayName(selected))">
            {{ getInitials(displayName(selected)) }}
          </AvatarFallback>
        </Avatar>
        <div class="min-w-0 flex-1">
          <p class="truncate text-sm font-medium text-white light:text-gray-900">{{ displayName(selected) }}</p>
          <p class="truncate text-[11px] text-white/50 light:text-gray-500">{{ selected.phone_number }}</p>
        </div>
        <Button variant="outline" size="sm" @click="router.push(`/chat/${selected.id}`)">
          <MessageSquare class="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />
          {{ t('chat.openConversation') }}
        </Button>
      </div>
      <ConversationNotes :key="selected.id" :contact-id="selected.id" embedded />
    </div>

    <div v-else class="hidden flex-1 items-center justify-center p-6 md:flex">
      <div class="max-w-xs text-center">
        <div class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-amber-500/15">
          <StickyNote class="h-7 w-7 text-amber-500" aria-hidden="true" />
        </div>
        <h2 class="mb-1 text-base font-semibold text-white light:text-gray-900">{{ t('chat.notesPickContact') }}</h2>
        <p class="text-sm text-white/50 light:text-gray-500">{{ t('chat.notesPickContactDesc') }}</p>
      </div>
    </div>
  </div>
</template>
