<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Palette, ImagePlus, Check, Trash2 } from 'lucide-vue-next'
import { useWallpaper, wallpaperPresets } from '@/composables/useWallpaper'
import { useAuthStore } from '@/stores/auth'

const { t } = useI18n()
const authStore = useAuthStore()
const { active, orgWallpaperUrl, select, useSystemImage, uploadOrgWallpaper, removeOrgWallpaper } = useWallpaper()
const fileInput = ref<HTMLInputElement | null>(null)

// Everyone may pick a preset; only a super admin may set the system image (the server enforces it too).
const isSuperAdmin = computed(() => authStore.user?.is_super_admin ?? false)

async function onFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  try {
    await uploadOrgWallpaper(file)
  } catch (err) {
    toast.error((err as Error).message === 'not-image' ? t('nav.wallpaperNotImage') : t('nav.wallpaperTooLarge'))
  }
}

async function onRemove() {
  try {
    await removeOrgWallpaper()
  } catch {
    toast.error(t('nav.wallpaperRemoveFailed'))
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
    <PopoverContent
      align="end"
      class="w-[264px] rounded-2xl border border-white/10 bg-neutral-900/95 p-4 text-white shadow-2xl backdrop-blur"
      data-testid="wallpaper-popover"
    >
      <div class="mb-3 text-[11px] font-semibold uppercase tracking-[0.14em] text-white/60">{{ $t('nav.wallpaperCustomize') }}</div>
      <div class="flex flex-wrap items-center gap-3" role="radiogroup" :aria-label="$t('nav.wallpaper')">
        <button
          v-if="orgWallpaperUrl"
          type="button"
          role="radio"
          data-testid="wallpaper-system"
          :aria-checked="active === 'system'"
          :aria-label="$t('nav.wallpaperSystem')"
          :title="$t('nav.wallpaperSystem')"
          class="swatch bg-cover bg-center"
          :class="active === 'system' && 'swatch-active'"
          :style="{ backgroundImage: `url(${orgWallpaperUrl})` }"
          @click="useSystemImage"
        >
          <Check v-if="active === 'system'" class="h-4 w-4 text-white drop-shadow" aria-hidden="true" />
        </button>
        <button
          v-for="p in wallpaperPresets"
          :key="p.id"
          type="button"
          role="radio"
          :aria-checked="active === p.id"
          :aria-label="p.name"
          :title="p.name"
          class="swatch"
          :class="active === p.id && 'swatch-active'"
          :style="{ background: p.css }"
          @click="select(p.id)"
        >
          <Check v-if="active === p.id" class="h-4 w-4 text-white drop-shadow" aria-hidden="true" />
        </button>
      </div>
      <button
        v-if="isSuperAdmin"
        type="button"
        data-testid="wallpaper-upload"
        class="mt-4 flex w-full items-center justify-center gap-2 rounded-full bg-white/10 px-4 py-2.5 text-sm font-semibold text-white transition hover:bg-white/15 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/60"
        @click="fileInput?.click()"
      >
        <ImagePlus class="h-4 w-4" aria-hidden="true" />
        {{ $t('nav.wallpaperUpload') }}
      </button>
      <button
        v-if="isSuperAdmin && orgWallpaperUrl"
        type="button"
        data-testid="wallpaper-remove"
        class="mt-3 flex items-center gap-1.5 text-[12px] text-white/60 hover:text-red-300"
        @click="onRemove"
      >
        <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
        {{ $t('nav.wallpaperRemove') }}
      </button>
      <p v-if="isSuperAdmin" class="mt-2 text-[11px] leading-snug text-white/50">{{ $t('nav.wallpaperAppliesToAll') }}</p>
      <input v-if="isSuperAdmin" ref="fileInput" type="file" accept="image/*" class="hidden" @change="onFile">
    </PopoverContent>
  </Popover>
</template>

<style scoped>
/* round colour swatches: a faint ring, and a white ring with a check on the one in use */
.swatch {
  display: inline-flex;
  height: 2.25rem;
  width: 2.25rem;
  align-items: center;
  justify-content: center;
  border-radius: 9999px;
  box-shadow: inset 0 0 0 1px rgb(255 255 255 / 0.18);
  transition: transform 0.15s ease, box-shadow 0.15s ease;
}
.swatch:hover { transform: scale(1.06); }
.swatch:focus-visible { outline: none; box-shadow: 0 0 0 2px rgb(255 255 255 / 0.7); }
.swatch-active { box-shadow: 0 0 0 2px #1d1d1f, 0 0 0 4px rgb(255 255 255 / 0.9); }
</style>
