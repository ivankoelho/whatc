<script setup lang="ts">
// The app shell is a system theme chosen by a super admin for everyone
// (Settings → Geral → Tema do sistema, stored in branding.ui_theme):
// Bitrix (default) or Clássico. Both render the same pages.
import { onMounted } from 'vue'
import { useBranding } from '@/composables/useBranding'
import BitrixLayout from './BitrixLayout.vue'
import ClassicLayout from './ClassicLayout.vue'

// Nothing renders until the theme is known (one quick public request; on
// failure loadBranding still flags it loaded and the default applies), so
// a Clássico system never flashes the Bitrix shell first.
const { uiTheme, loadBranding, brandingLoaded } = useBranding()
onMounted(loadBranding)
</script>

<template>
  <template v-if="brandingLoaded">
    <ClassicLayout v-if="uiTheme === 'classic'" />
    <BitrixLayout v-else />
  </template>
</template>
