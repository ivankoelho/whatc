<script setup lang="ts">
// Phone-only quick navigation (hidden from md up). Shared by both system
// themes: the layout passes the first permitted modules; "More" opens its
// existing drawer, where every other module lives.
import { RouterLink } from 'vue-router'
import { MoreHorizontal } from 'lucide-vue-next'
import { markRaw, type Component } from 'vue'

defineProps<{
  items: Array<{ name: string; path: string; icon: Component; active?: boolean }>
  moreOpen: boolean
  /** Glass look for the Bitrix wallpaper shell. */
  glass?: boolean
}>()
defineEmits<{ more: [] }>()
</script>

<template>
  <nav
    :class="[
      'fixed inset-x-0 bottom-0 z-50 flex border-t pb-[env(safe-area-inset-bottom)] md:hidden',
      glass
        ? 'shell-glass border-white/15 bg-black/55 text-white backdrop-blur-xl'
        : 'border-white/[0.08] light:border-gray-200 bg-[#0a0a0b]/95 light:bg-white/95 backdrop-blur-sm'
    ]"
    :aria-label="$t('nav.mainNavigation')"
  >
    <RouterLink
      v-for="item in items"
      :key="item.path"
      :to="item.path"
      :class="[
        'flex h-14 min-w-0 flex-1 flex-col items-center justify-center gap-0.5 text-[10px] font-medium transition active:scale-95',
        item.active ? 'text-emerald-400' : glass ? 'text-white/65' : 'text-white/50 light:text-gray-500'
      ]"
      :aria-current="item.active ? 'page' : undefined"
    >
      <component :is="markRaw(item.icon)" class="h-5 w-5" aria-hidden="true" />
      <span class="max-w-full truncate px-1">{{ $t(item.name) }}</span>
    </RouterLink>
    <button
      type="button"
      :class="[
        'flex h-14 min-w-0 flex-1 flex-col items-center justify-center gap-0.5 text-[10px] font-medium transition active:scale-95',
        moreOpen ? 'text-emerald-400' : glass ? 'text-white/65' : 'text-white/50 light:text-gray-500'
      ]"
      :aria-expanded="moreOpen"
      @click="$emit('more')"
    >
      <MoreHorizontal class="h-5 w-5" aria-hidden="true" />
      <span>{{ $t('nav.more') }}</span>
    </button>
  </nav>
</template>
