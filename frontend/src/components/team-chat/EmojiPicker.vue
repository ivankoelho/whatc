<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { EMOJI_CATEGORIES } from '@/lib/team-chat-emoji'

// A small emoji panel for the composer: category tabs and a grid. It only reports the
// chosen glyph; the parent decides where it goes (at the caret) and when to close.
const emit = defineEmits<{ pick: [glyph: string] }>()
const { t } = useI18n()
const active = ref(EMOJI_CATEGORIES[0].id)
</script>

<template>
  <div
    class="w-[min(24rem,calc(100vw-1rem))] rounded-md border bg-popover p-2 shadow-md"
    role="dialog" :aria-label="t('teamChat.emoji.title')" data-testid="emoji-picker"
  >
    <div class="mb-2 flex gap-1 border-b pb-1" role="tablist">
      <button
        v-for="c in EMOJI_CATEGORIES" :key="c.id" type="button" role="tab"
        :aria-selected="active === c.id" :data-testid="`emoji-tab-${c.id}`"
        :class="['rounded px-2 py-1.5 text-xs md:py-1', active === c.id ? 'bg-muted font-medium' : 'text-muted-foreground hover:bg-muted']"
        @click="active = c.id"
      >{{ t(`teamChat.emoji.categories.${c.id}`) }}</button>
    </div>
    <div class="grid max-h-48 grid-cols-8 gap-0.5 overflow-y-auto" role="group" :aria-label="t(`teamChat.emoji.categories.${active}`)">
      <template v-for="c in EMOJI_CATEGORIES" :key="c.id">
        <button
          v-for="e in (c.id === active ? c.items : [])" :key="e" type="button"
          class="flex h-10 w-full items-center justify-center rounded text-xl hover:bg-muted md:h-8"
          :aria-label="e" :data-testid="`emoji-${e}`" @click="emit('pick', e)"
        >{{ e }}</button>
      </template>
    </div>
  </div>
</template>
