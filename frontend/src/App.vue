<script setup lang="ts">
import { RouterView } from 'vue-router'
import { Toaster } from 'vue-sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useColorMode } from '@/composables/useColorMode'

// Initialize color mode early
const { colorMode } = useColorMode()
</script>

<template>
  <TooltipProvider>
    <div class="min-h-screen bg-background font-sans antialiased">
      <RouterView />
      <!-- Bottom-right: at the top the toasts covered the page header actions (e.g. Delete). -->
      <Toaster position="bottom-right" richColors :theme="colorMode" />
    </div>
  </TooltipProvider>
</template>
