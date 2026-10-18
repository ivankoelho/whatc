<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Palette, ImagePlus, Check, Trash2 } from 'lucide-vue-next'
import { useWallpaper, wallpaperPresets } from '@/composables/useWallpaper'

const { t } = useI18n()
const { selected, customUrl, select, upload, removeCustom } = useWallpaper()
const fileInput = ref<HTMLInputElement | null>(null)

async function onFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  try {
    await upload(file)
  } catch (err) {
    toast.error((err as Error).message === 'not-image' ? t('nav.wallpaperNotImage') : t('nav.wallpaperTooLarge'))
  }
}
</script>

<template>
  <Popover>
    <PopoverTrigger as-child>
      <button
        type="button"
        class="shell-chip h-8 w-8 justify-center"
        :aria-label="$t('nav.wallpaper')"
        :title="$t('nav.wallpaper')"
      >
        <Palette class="h-4 w-4" aria-hidden="true" />
      </button>
    </PopoverTrigger>
    <PopoverContent align="end" class="w-72 p-3">
      <div class="mb-2 text-[13px] font-semibold">{{ $t('nav.wallpaper') }}</div>
      <div class="grid grid-cols-3 gap-2" role="radiogroup" :aria-label="$t('nav.wallpaper')">
        <button
          v-for="p in wallpaperPresets"
          :key="p.id"
          type="button"
          role="radio"
          :aria-checked="selected === p.id"
          :aria-label="p.name"
          :title="p.name"
          class="relative aspect-[4/3] rounded-lg ring-offset-2 ring-offset-popover transition hover:scale-[1.03] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          :class="selected === p.id && 'ring-2 ring-primary'"
          :style="{ background: p.css }"
          @click="select(p.id)"
        >
          <Check v-if="selected === p.id" class="absolute right-1 top-1 h-3.5 w-3.5 text-white drop-shadow" aria-hidden="true" />
        </button>
        <button
          v-if="customUrl"
          type="button"
          role="radio"
          :aria-checked="selected === 'custom'"
          :aria-label="$t('nav.wallpaperCustom')"
          class="relative aspect-[4/3] rounded-lg bg-cover bg-center ring-offset-2 ring-offset-popover transition hover:scale-[1.03]"
          :class="selected === 'custom' && 'ring-2 ring-primary'"
          :style="{ backgroundImage: `url(${customUrl})` }"
          @click="select('custom')"
        >
          <Check v-if="selected === 'custom'" class="absolute right-1 top-1 h-3.5 w-3.5 text-white drop-shadow" aria-hidden="true" />
        </button>
        <button
          type="button"
          class="flex aspect-[4/3] flex-col items-center justify-center gap-1 rounded-lg border border-dashed text-[11px] text-muted-foreground transition hover:border-primary hover:text-foreground"
          @click="fileInput?.click()"
        >
          <ImagePlus class="h-4 w-4" aria-hidden="true" />
          {{ $t('nav.wallpaperUpload') }}
        </button>
      </div>
      <button
        v-if="customUrl"
        type="button"
        class="mt-3 flex items-center gap-1.5 text-[12px] text-muted-foreground hover:text-destructive"
        @click="removeCustom"
      >
        <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
        {{ $t('nav.wallpaperRemove') }}
      </button>
      <input ref="fileInput" type="file" accept="image/*" class="hidden" @change="onFile">
    </PopoverContent>
  </Popover>
</template>
