<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { PageHeader, AuditLogPanel } from '@/components/shared'
import LanguageSwitcher from '@/components/LanguageSwitcher.vue'
import UnitsView from './UnitsView.vue'
import DepartmentsView from './DepartmentsView.vue'
import { resolveActiveTab, type SettingsTabConfig } from '@/lib/settings-tab-hub'
import { toast } from 'vue-sonner'
import { Settings, Loader2, Globe, Phone, Upload, Play, Pause, Music, Image as ImageIcon } from 'lucide-vue-next'
import { usersService, organizationService, brandingService } from '@/services/api'
import { useAuthStore } from '@/stores/auth'

const { t } = useI18n()
const authStore = useAuthStore()
const route = useRoute()
const router = useRouter()

// Geral | Notificações | Chamadas | Unidades | Departamentos. Units and departments are
// structural, system-wide catalogs (contacts, users, sales, X2...), so they live here and not
// under Ocorrências. Each tab shows for its own read permission; the API stays the authority.
const SETTINGS_TABS = ['general', 'notifications', 'calling']
const tabConfigs: SettingsTabConfig[] = [
  { value: 'general', permission: 'settings.general' },
  { value: 'notifications', permission: 'settings.general' },
  { value: 'calling', permission: 'settings.general' },
  { value: 'units', permission: 'units' },
  { value: 'departments', permission: 'departments' },
]
// The units/departments lists are served under occurrences:read (agents use them without
// units:read/departments:read), so a tab is only useful to someone who also has that.
const needsListAccess = ['units', 'departments']
const canRead = (permission: string) =>
  authStore.hasPermission(permission, 'read')
  && (!needsListAccess.includes(permission) || authStore.hasPermission('occurrences', 'read'))
const canSee = (value: string) => {
  const tab = tabConfigs.find(c => c.value === value)
  return !!tab && canRead(tab.permission)
}
const activeTab = computed(() =>
  resolveActiveTab(
    tabConfigs,
    typeof route.query.tab === 'string' ? route.query.tab : undefined,
    'general',
    canRead,
  ),
)
const isSettingsTab = computed(() => !!activeTab.value && SETTINGS_TABS.includes(activeTab.value))
// Keep ?tab= honest, same as the other settings hubs.
watch(activeTab, (value) => {
  if (value && route.query.tab !== value) {
    router.replace({ query: { ...route.query, tab: value } })
  }
}, { immediate: true })
function onTabChange(value: string | number) {
  router.replace({ query: { ...route.query, tab: String(value) } })
}

// The active org may be overridden by the X-Organization-ID header
// (localStorage.selected_organization_id) when a super admin switches orgs.
// That override is what the backend uses for scoping, so we must read it here
// too — otherwise the activity log panel would query the user's default org
// instead of the currently-active one.
const orgID = computed(
  () => localStorage.getItem('selected_organization_id') || authStore.organizationId,
)
const userID = computed(() => authStore.user?.id || '')
const canWriteAccounts = computed(() => authStore.hasPermission('accounts', 'write'))
const isSuperAdmin = computed(() => authStore.user?.is_super_admin ?? false)

const isSubmitting = ref(false)
const isLoading = ref(true)

// General Settings
const generalSettings = ref({
  organization_name: 'My Organization',
  default_timezone: 'America/Bahia',
  date_format: 'YYYY-MM-DD',
  mask_phone_numbers: false,
  meta_app_id: '',
  meta_config_id: '',
  meta_app_secret: '',
  has_meta_app_secret: false
})

// Notification Settings
const notificationSettings = ref({
  email_notifications: true,
  new_message_alerts: true,
  campaign_updates: true
})

// Calling Settings
const callingSettings = ref({
  calling_enabled: false,
  max_call_duration: 300,
  transfer_timeout_secs: 120,
  hold_music_file: '',
  ringback_file: ''
})

const isUploadingHoldMusic = ref(false)
const isUploadingRingback = ref(false)
const holdMusicInput = ref<HTMLInputElement | null>(null)
const ringbackInput = ref<HTMLInputElement | null>(null)
const holdMusicAudio = ref<HTMLAudioElement | null>(null)
const ringbackAudio = ref<HTMLAudioElement | null>(null)
const playingHoldMusic = ref(false)
const playingRingback = ref(false)

const loginBackgroundUrl = ref<string | null>(null)
const isUploadingLoginBackground = ref(false)
const isRemovingLoginBackground = ref(false)
const loginBackgroundInput = ref<HTMLInputElement | null>(null)

const logoUrl = ref<string | null>(null)
const isUploadingLogo = ref(false)
const isRemovingLogo = ref(false)
const logoInput = ref<HTMLInputElement | null>(null)
const systemName = ref('')
const isSavingSystemName = ref(false)

const footerSettings = ref({ footer_text: '', footer_version: '' })
const isSavingFooter = ref(false)

// Bump these keys to force the AuditLogPanel to remount and refetch after a save.
// The backend writes audit entries asynchronously in a goroutine, so we delay
// the remount slightly to give the write time to hit the DB before refetching.
const generalLogKey = ref(0)
const notificationLogKey = ref(0)
const callingLogKey = ref(0)

function refreshActivityLog(key: typeof generalLogKey) {
  setTimeout(() => { key.value++ }, 500)
}

// Backend-emitted branding URLs are root-absolute; prefix the base path the
// same way every other media URL in this codebase does (see ChatView.vue's
// getMediaUrl, MediaViewerDialog.vue) so it resolves under a base_path deploy.
function withBasePath(url: string): string {
  const basePath = ((window as any).__BASE_PATH__ ?? '').replace(/\/$/, '')
  return `${basePath}${url}`
}

onMounted(async () => {
  // Branding is a purely cosmetic, optional config -- isolated with its own
  // try/catch (same principle as LoginView.vue's onMounted) so a failure
  // there can never take down the rest of this page via a shared Promise.all.
  ;(async () => {
    try {
      const response = await brandingService.getPublic()
      const data = response.data.data || response.data
      const url = data?.login_background_url ?? null
      loginBackgroundUrl.value = url ? withBasePath(url) : null
      const logo = data?.logo_url ?? null
      logoUrl.value = logo ? withBasePath(logo) : null
      systemName.value = data?.system_name ?? ''
      footerSettings.value = {
        footer_text: data?.footer_text ?? '',
        footer_version: data?.footer_version ?? '',
      }
    } catch {
      loginBackgroundUrl.value = null
    }
  })()

  try {
    const [orgResponse, userResponse] = await Promise.all([
      organizationService.getSettings(),
      usersService.me()
    ])

    // Organization settings
    const orgData = orgResponse.data.data || orgResponse.data
    if (orgData) {
      generalSettings.value = {
        organization_name: orgData.name || 'My Organization',
        default_timezone: orgData.settings?.timezone || 'UTC',
        date_format: orgData.settings?.date_format || 'YYYY-MM-DD',
        mask_phone_numbers: orgData.settings?.mask_phone_numbers || false,
        meta_app_id: orgData.settings?.meta_app_id || '',
        meta_config_id: orgData.settings?.meta_config_id || '',
        meta_app_secret: '',
        has_meta_app_secret: orgData.settings?.has_meta_app_secret || false
      }
      callingSettings.value = {
        calling_enabled: orgData.settings?.calling_enabled || false,
        max_call_duration: orgData.settings?.max_call_duration || 300,
        transfer_timeout_secs: orgData.settings?.transfer_timeout_secs || 120,
        hold_music_file: orgData.settings?.hold_music_file || '',
        ringback_file: orgData.settings?.ringback_file || ''
      }
    }

    // User notification settings
    const user = userResponse.data.data || userResponse.data
    if (user.settings) {
      notificationSettings.value = {
        email_notifications: user.settings.email_notifications ?? true,
        new_message_alerts: user.settings.new_message_alerts ?? true,
        campaign_updates: user.settings.campaign_updates ?? true
      }
    }
  } catch (error) {
    console.error('Failed to load settings:', error)
  } finally {
    isLoading.value = false
  }
})

async function saveGeneralSettings() {
  isSubmitting.value = true
  try {
    const payload: any = {
      name: generalSettings.value.organization_name,
      timezone: generalSettings.value.default_timezone,
      date_format: generalSettings.value.date_format,
      mask_phone_numbers: generalSettings.value.mask_phone_numbers
    }
    if (canWriteAccounts.value) {
      payload.meta_app_id = generalSettings.value.meta_app_id
      payload.meta_config_id = generalSettings.value.meta_config_id
      if (generalSettings.value.meta_app_secret) {
        payload.meta_app_secret = generalSettings.value.meta_app_secret
      }
    }
    await organizationService.updateSettings(payload)
    toast.success(t('settings.generalSaved'))
    // Clear secret input after save
    generalSettings.value.meta_app_secret = ''
    // Refresh organization settings to update has_meta_app_secret status
    const orgResponse = await organizationService.getSettings()
    const orgData = orgResponse.data.data || orgResponse.data
    if (orgData) {
      generalSettings.value.has_meta_app_secret = orgData.settings?.has_meta_app_secret || false
    }
    refreshActivityLog(generalLogKey)
  } catch (error) {
    toast.error(t('common.failedSave', { resource: t('resources.settings') }))
  } finally {
    isSubmitting.value = false
  }
}

async function saveNotificationSettings() {
  isSubmitting.value = true
  try {
    await usersService.updateSettings({
      email_notifications: notificationSettings.value.email_notifications,
      new_message_alerts: notificationSettings.value.new_message_alerts,
      campaign_updates: notificationSettings.value.campaign_updates
    })
    toast.success(t('settings.notificationsSaved'))
    refreshActivityLog(notificationLogKey)
  } catch (error) {
    toast.error(t('common.failedSave', { resource: t('resources.notificationSettings') }))
  } finally {
    isSubmitting.value = false
  }
}

async function saveCallingSettings() {
  isSubmitting.value = true
  try {
    await organizationService.updateSettings({
      calling_enabled: callingSettings.value.calling_enabled,
      max_call_duration: callingSettings.value.max_call_duration,
      transfer_timeout_secs: callingSettings.value.transfer_timeout_secs
    })
    toast.success(t('settings.callingSaved'))
    refreshActivityLog(callingLogKey)
  } catch (error) {
    toast.error(t('common.failedSave', { resource: t('resources.settings') }))
  } finally {
    isSubmitting.value = false
  }
}

async function uploadAudio(type: 'hold_music' | 'ringback', event: Event) {
  const input = event.target as HTMLInputElement
  const file = input?.files?.[0]
  if (!file) return

  const isHold = type === 'hold_music'
  if (isHold) isUploadingHoldMusic.value = true
  else isUploadingRingback.value = true

  try {
    const response = await organizationService.uploadOrgAudio(file, type)
    const data = response.data.data || response.data
    if (isHold) callingSettings.value.hold_music_file = data.filename
    else callingSettings.value.ringback_file = data.filename
    toast.success(t('settings.audioUploaded'))
  } catch (error) {
    toast.error(t('settings.audioUploadFailed'))
  } finally {
    if (isHold) isUploadingHoldMusic.value = false
    else isUploadingRingback.value = false
    input.value = ''
  }
}

async function uploadLoginBackground(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input?.files?.[0]
  if (!file) return

  isUploadingLoginBackground.value = true
  try {
    const response = await brandingService.uploadLoginBackground(file)
    const data = response.data.data || response.data
    loginBackgroundUrl.value = data.login_background_url ? withBasePath(data.login_background_url) : null
    toast.success(t('settings.loginBackgroundUploaded'))
    refreshActivityLog(generalLogKey)
  } catch (error) {
    toast.error(t('settings.loginBackgroundUploadFailed'))
  } finally {
    isUploadingLoginBackground.value = false
    input.value = ''
  }
}

async function removeLoginBackground() {
  isRemovingLoginBackground.value = true
  try {
    await brandingService.deleteLoginBackground()
    loginBackgroundUrl.value = null
    toast.success(t('settings.loginBackgroundRemoved'))
    refreshActivityLog(generalLogKey)
  } catch (error) {
    toast.error(t('settings.loginBackgroundRemoveFailed'))
  } finally {
    isRemovingLoginBackground.value = false
  }
}

async function saveFooter() {
  isSavingFooter.value = true
  try {
    await brandingService.updateFooter(footerSettings.value)
    toast.success(t('settings.loginFooterSaved'))
    refreshActivityLog(generalLogKey)
  } catch (error) {
    toast.error(t('settings.loginFooterSaveFailed'))
  } finally {
    isSavingFooter.value = false
  }
}

async function uploadLogo(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input?.files?.[0]
  if (!file) return

  isUploadingLogo.value = true
  try {
    const response = await brandingService.uploadLogo(file)
    const data = response.data.data || response.data
    logoUrl.value = data.logo_url ? withBasePath(data.logo_url) : null
    toast.success(t('settings.logoUploaded'))
    refreshActivityLog(generalLogKey)
  } catch (error) {
    toast.error(t('settings.logoUploadFailed'))
  } finally {
    isUploadingLogo.value = false
    input.value = ''
  }
}

async function removeLogo() {
  isRemovingLogo.value = true
  try {
    await brandingService.deleteLogo()
    logoUrl.value = null
    toast.success(t('settings.logoRemoved'))
    refreshActivityLog(generalLogKey)
  } catch (error) {
    toast.error(t('settings.logoRemoveFailed'))
  } finally {
    isRemovingLogo.value = false
  }
}

async function saveSystemName() {
  isSavingSystemName.value = true
  try {
    await brandingService.updateFooter({ system_name: systemName.value })
    toast.success(t('settings.logoSaved'))
    refreshActivityLog(generalLogKey)
  } catch (error) {
    toast.error(t('settings.logoSaveFailed'))
  } finally {
    isSavingSystemName.value = false
  }
}

function togglePlayAudio(type: 'hold_music' | 'ringback') {
  const isHold = type === 'hold_music'
  const filename = isHold ? callingSettings.value.hold_music_file : callingSettings.value.ringback_file
  if (!filename) return

  const audioRef = isHold ? holdMusicAudio : ringbackAudio
  const playingRef = isHold ? playingHoldMusic : playingRingback

  if (playingRef.value && audioRef.value) {
    audioRef.value.pause()
    audioRef.value.currentTime = 0
    playingRef.value = false
    return
  }

  const audio = new Audio(`/api/ivr-flows/audio/${filename}`)
  audioRef.value = audio
  playingRef.value = true
  audio.play()
  audio.onended = () => { playingRef.value = false }
}
</script>

<template>
  <div class="flex flex-col h-full bg-[#0a0a0b] light:bg-gray-50">
    <div v-if="!activeTab" class="p-6 text-sm text-white/50 light:text-gray-500">{{ $t('common.noAccessToSection') }}</div>
    <Tabs v-else :model-value="activeTab" class="flex h-full min-h-0 w-full flex-col" @update:model-value="onTabChange">
      <TabsList class="h-12 w-full shrink-0 justify-start gap-6 overflow-x-auto overflow-y-hidden rounded-none bg-transparent p-0 px-6 shadow-[inset_0_-1px_0_0_rgba(255,255,255,0.08)] [scrollbar-width:none] light:shadow-[inset_0_-1px_0_0_#e5e7eb] [&::-webkit-scrollbar]:hidden">
        <TabsTrigger v-if="canSee('general')" value="general" class="h-12 rounded-none border-b-2 border-transparent bg-transparent px-0 text-[13px] text-white/50 shadow-none hover:text-white/80 data-[state=active]:border-emerald-500 data-[state=active]:bg-transparent data-[state=active]:text-white data-[state=active]:shadow-none light:text-gray-500 light:hover:text-gray-800 light:data-[state=active]:text-gray-900">{{ $t('settings.general') }}</TabsTrigger>
        <TabsTrigger v-if="canSee('notifications')" value="notifications" class="h-12 rounded-none border-b-2 border-transparent bg-transparent px-0 text-[13px] text-white/50 shadow-none hover:text-white/80 data-[state=active]:border-emerald-500 data-[state=active]:bg-transparent data-[state=active]:text-white data-[state=active]:shadow-none light:text-gray-500 light:hover:text-gray-800 light:data-[state=active]:text-gray-900">{{ $t('settings.notifications') }}</TabsTrigger>
        <TabsTrigger v-if="canSee('calling')" value="calling" class="h-12 rounded-none border-b-2 border-transparent bg-transparent px-0 text-[13px] text-white/50 shadow-none hover:text-white/80 data-[state=active]:border-emerald-500 data-[state=active]:bg-transparent data-[state=active]:text-white data-[state=active]:shadow-none light:text-gray-500 light:hover:text-gray-800 light:data-[state=active]:text-gray-900">{{ $t('settings.calling') }}</TabsTrigger>
        <TabsTrigger v-if="canSee('units')" value="units" class="h-12 rounded-none border-b-2 border-transparent bg-transparent px-0 text-[13px] text-white/50 shadow-none hover:text-white/80 data-[state=active]:border-emerald-500 data-[state=active]:bg-transparent data-[state=active]:text-white data-[state=active]:shadow-none light:text-gray-500 light:hover:text-gray-800 light:data-[state=active]:text-gray-900">{{ $t('nav.units') }}</TabsTrigger>
        <TabsTrigger v-if="canSee('departments')" value="departments" class="h-12 rounded-none border-b-2 border-transparent bg-transparent px-0 text-[13px] text-white/50 shadow-none hover:text-white/80 data-[state=active]:border-emerald-500 data-[state=active]:bg-transparent data-[state=active]:text-white data-[state=active]:shadow-none light:text-gray-500 light:hover:text-gray-800 light:data-[state=active]:text-gray-900">{{ $t('nav.departments') }}</TabsTrigger>
      </TabsList>

      <template v-if="isSettingsTab">
    <PageHeader :title="$t('settings.title')" :subtitle="$t('settings.subtitle')" :icon="Settings" icon-gradient="bg-gradient-to-br from-gray-500 to-gray-600 shadow-gray-500/20" />
    <ScrollArea class="flex-1">
      <div class="p-6 space-y-4 max-w-4xl mx-auto">
        <div class="w-full">
          <!-- General Settings Tab -->
          <TabsContent value="general">
            <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.generalSettings') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.generalSettingsDesc') }}</p>
              </div>
              <div class="p-6 pt-3 space-y-4">
                <div class="space-y-2">
                  <Label for="org_name" class="text-white/70 light:text-gray-700">{{ $t('settings.organizationName') }}</Label>
                  <Input
                    id="org_name"
                    v-model="generalSettings.organization_name"
                    :placeholder="$t('settings.organizationPlaceholder')"
                  />
                </div>
                <div class="grid grid-cols-2 gap-4">
                  <div class="space-y-2">
                    <Label for="timezone" class="text-white/70 light:text-gray-700">{{ $t('settings.defaultTimezone') }}</Label>
                    <Select v-model="generalSettings.default_timezone">
                      <SelectTrigger class="bg-white/[0.04] border-white/[0.1] text-white/70 light:bg-white light:border-gray-200 light:text-gray-700">
                        <SelectValue :placeholder="$t('settings.selectTimezone')" />
                      </SelectTrigger>
                      <SelectContent class="bg-[#141414] border-white/[0.08] light:bg-white light:border-gray-200">
                        <SelectItem value="America/Bahia" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">Bahia (Brasília, UTC-3)</SelectItem>
                        <SelectItem value="America/Manaus" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">Manaus (UTC-4)</SelectItem>
                        <SelectItem value="America/Rio_Branco" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">Rio Branco (UTC-5)</SelectItem>
                        <SelectItem value="America/Noronha" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">Fernando de Noronha (UTC-2)</SelectItem>
                        <SelectItem value="UTC" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">UTC</SelectItem>
                        <SelectItem value="America/New_York" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">Eastern Time</SelectItem>
                        <SelectItem value="America/Los_Angeles" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">Pacific Time</SelectItem>
                        <SelectItem value="Europe/London" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">London</SelectItem>
                        <SelectItem value="Asia/Tokyo" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">Tokyo</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  <div class="space-y-2">
                    <Label for="date_format" class="text-white/70 light:text-gray-700">{{ $t('settings.dateFormat') }}</Label>
                    <Select v-model="generalSettings.date_format">
                      <SelectTrigger class="bg-white/[0.04] border-white/[0.1] text-white/70 light:bg-white light:border-gray-200 light:text-gray-700">
                        <SelectValue :placeholder="$t('settings.selectFormat')" />
                      </SelectTrigger>
                      <SelectContent class="bg-[#141414] border-white/[0.08] light:bg-white light:border-gray-200">
                        <SelectItem value="YYYY-MM-DD" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">YYYY-MM-DD</SelectItem>
                        <SelectItem value="DD/MM/YYYY" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">DD/MM/YYYY</SelectItem>
                        <SelectItem value="MM/DD/YYYY" class="text-white/70 focus:bg-white/[0.08] focus:text-white light:text-gray-700 light:focus:bg-gray-100">MM/DD/YYYY</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </div>
                <div class="space-y-2">
                  <Label class="text-white/70 light:text-gray-700">
                    <Globe class="h-4 w-4 inline mr-1" />
                    {{ $t('settings.language') }}
                  </Label>
                  <LanguageSwitcher class="max-w-xs" />
                  <p class="text-xs text-white/40 light:text-gray-500">{{ $t('settings.languageDesc') }}</p>
                </div>
                <Separator class="bg-white/[0.08] light:bg-gray-200" />
                <div class="flex items-center justify-between">
                  <div>
                    <p class="font-medium text-white light:text-gray-900">{{ $t('settings.maskPhoneNumbers') }}</p>
                    <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.maskPhoneNumbersDesc') }}</p>
                  </div>
                  <Switch
                    :checked="generalSettings.mask_phone_numbers"
                    @update:checked="generalSettings.mask_phone_numbers = $event"
                  />
                </div>
                <div class="flex justify-end">
                  <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="saveGeneralSettings" :disabled="isSubmitting">
                    <Loader2 v-if="isSubmitting" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('settings.save') }}
                  </Button>
                </div>
              </div>
            </div>

            <!-- Login Background Card (Gated on isSuperAdmin -- branding_settings is a
                 system-wide singleton, not per-organization; see branding.go) -->
            <div v-if="isSuperAdmin" class="mt-6 rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.loginBackground') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.loginBackgroundDesc') }}</p>
              </div>
              <div class="p-6 pt-3 space-y-3">
                <div v-if="loginBackgroundUrl" class="rounded-lg overflow-hidden border border-white/[0.08] light:border-gray-200 h-32 w-full max-w-sm">
                  <img :src="loginBackgroundUrl" :alt="$t('settings.loginBackground')" class="h-full w-full object-cover" />
                </div>
                <p v-else class="text-sm text-white/50 light:text-gray-500">{{ $t('settings.noFileUploaded') }}</p>
                <div class="flex items-center gap-2">
                  <input ref="loginBackgroundInput" type="file" accept="image/jpeg,image/png,image/webp" class="hidden" @change="uploadLoginBackground" />
                  <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="loginBackgroundInput?.click()" :disabled="isUploadingLoginBackground">
                    <Loader2 v-if="isUploadingLoginBackground" class="mr-2 h-4 w-4 animate-spin" />
                    <Upload v-else class="mr-2 h-4 w-4" />
                    {{ $t('settings.uploadImage') }}
                  </Button>
                  <Button
                    v-if="loginBackgroundUrl"
                    variant="ghost"
                    size="sm"
                    class="text-white/50 hover:text-white light:text-gray-500 light:hover:text-gray-900"
                    @click="removeLoginBackground"
                    :disabled="isRemovingLoginBackground"
                  >
                    <Loader2 v-if="isRemovingLoginBackground" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('common.remove') }}
                  </Button>
                  <span class="text-xs text-white/30 light:text-gray-400">.jpg, .png, .webp (max 5MB)</span>
                </div>
              </div>
            </div>

            <!-- System Logo & Name Card (Gated on isSuperAdmin, same system-wide-singleton reasoning as Login Background) -->
            <div v-if="isSuperAdmin" class="mt-6 rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.systemLogo') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.systemLogoDesc') }}</p>
              </div>
              <div class="p-6 pt-3 space-y-4">
                <div class="flex items-center gap-3">
                  <div v-if="logoUrl" class="rounded-lg overflow-hidden border border-white/[0.08] light:border-gray-200 h-16 w-16 shrink-0 bg-white/[0.02]">
                    <img :src="logoUrl" :alt="$t('settings.systemLogo')" class="h-full w-full object-cover" />
                  </div>
                  <div v-else class="rounded-lg border border-dashed border-white/[0.15] light:border-gray-300 h-16 w-16 shrink-0 flex items-center justify-center">
                    <ImageIcon class="h-6 w-6 text-white/30 light:text-gray-400" />
                  </div>
                  <div class="flex items-center gap-2">
                    <input ref="logoInput" type="file" accept="image/png" class="hidden" @change="uploadLogo" />
                    <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="logoInput?.click()" :disabled="isUploadingLogo">
                      <Loader2 v-if="isUploadingLogo" class="mr-2 h-4 w-4 animate-spin" />
                      <Upload v-else class="mr-2 h-4 w-4" />
                      {{ $t('settings.uploadImage') }}
                    </Button>
                    <Button
                      v-if="logoUrl"
                      variant="ghost"
                      size="sm"
                      class="text-white/50 hover:text-white light:text-gray-500 light:hover:text-gray-900"
                      @click="removeLogo"
                      :disabled="isRemovingLogo"
                    >
                      <Loader2 v-if="isRemovingLogo" class="mr-2 h-4 w-4 animate-spin" />
                      {{ $t('common.remove') }}
                    </Button>
                  </div>
                </div>
                <p class="text-xs text-white/30 light:text-gray-400">.png (max 5MB)</p>
                <Separator class="bg-white/[0.08] light:bg-gray-200" />
                <div class="space-y-2 max-w-sm">
                  <Label for="system_name" class="text-white/70 light:text-gray-700">{{ $t('settings.systemName') }}</Label>
                  <Input id="system_name" v-model="systemName" :placeholder="$t('settings.systemNamePlaceholder')" />
                  <p class="text-xs text-white/40 light:text-gray-500">{{ $t('settings.systemNameDesc') }}</p>
                </div>
                <div class="flex justify-end">
                  <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="saveSystemName" :disabled="isSavingSystemName">
                    <Loader2 v-if="isSavingSystemName" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('settings.save') }}
                  </Button>
                </div>
              </div>
            </div>

            <!-- Login Footer Card (Gated on isSuperAdmin, same system-wide-singleton reasoning as Login Background) -->
            <div v-if="isSuperAdmin" class="mt-6 rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.loginFooter') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.loginFooterDesc') }}</p>
              </div>
              <div class="p-6 pt-3 space-y-4">
                <div class="grid grid-cols-2 gap-4">
                  <div class="space-y-2">
                    <Label for="footer_text" class="text-white/70 light:text-gray-700">{{ $t('settings.footerText') }}</Label>
                    <Input id="footer_text" v-model="footerSettings.footer_text" :placeholder="$t('settings.footerTextPlaceholder')" />
                  </div>
                  <div class="space-y-2">
                    <Label for="footer_version" class="text-white/70 light:text-gray-700">{{ $t('settings.footerVersion') }}</Label>
                    <Input id="footer_version" v-model="footerSettings.footer_version" :placeholder="$t('settings.footerVersionPlaceholder')" />
                  </div>
                </div>
                <div class="flex justify-end">
                  <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="saveFooter" :disabled="isSavingFooter">
                    <Loader2 v-if="isSavingFooter" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('settings.save') }}
                  </Button>
                </div>
              </div>
            </div>

            <!-- Meta App Credentials Card (Gated on canWriteAccounts) -->
            <div v-if="canWriteAccounts" class="mt-6 rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.metaAppCredentials') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.metaAppCredentialsDesc') }}</p>
              </div>
              <div class="p-6 pt-3 space-y-4">
                <div class="grid grid-cols-2 gap-4">
                  <div class="space-y-2">
                    <Label for="meta_app_id" class="text-white/70 light:text-gray-700">{{ $t('settings.metaAppId') }}</Label>
                    <Input
                      id="meta_app_id"
                      v-model="generalSettings.meta_app_id"
                      placeholder="e.g. 123456789012345"
                    />
                  </div>
                  <div class="space-y-2">
                    <Label for="meta_config_id" class="text-white/70 light:text-gray-700">{{ $t('settings.metaConfigId') }}</Label>
                    <Input
                      id="meta_config_id"
                      v-model="generalSettings.meta_config_id"
                      placeholder="e.g. 987654321098765"
                    />
                  </div>
                </div>
                <div class="space-y-2">
                  <Label for="meta_app_secret" class="text-white/70 light:text-gray-700">{{ $t('settings.metaAppSecret') }}</Label>
                  <Input
                    id="meta_app_secret"
                    type="password"
                    v-model="generalSettings.meta_app_secret"
                    :placeholder="generalSettings.has_meta_app_secret ? '••••••••••••' : 'Enter Meta App Secret'"
                  />
                </div>
                <div class="flex justify-end">
                  <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="saveGeneralSettings" :disabled="isSubmitting">
                    <Loader2 v-if="isSubmitting" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('settings.save') }}
                  </Button>
                </div>
              </div>
            </div>
            <div v-if="orgID" class="mt-4">
              <AuditLogPanel :key="generalLogKey" resource-type="settings.general" :resource-id="orgID" />
            </div>
          </TabsContent>

          <!-- Notification Settings Tab -->
          <TabsContent value="notifications">
            <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.notifications') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.notificationsDesc') }}</p>
              </div>
              <div class="p-6 pt-3 space-y-4">
                <div class="flex items-center justify-between">
                  <div>
                    <p class="font-medium text-white light:text-gray-900">{{ $t('settings.emailNotifications') }}</p>
                    <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.emailNotificationsDesc') }}</p>
                  </div>
                  <Switch
                    :checked="notificationSettings.email_notifications"
                    @update:checked="notificationSettings.email_notifications = $event"
                  />
                </div>
                <Separator class="bg-white/[0.08] light:bg-gray-200" />
                <div class="flex items-center justify-between">
                  <div>
                    <p class="font-medium text-white light:text-gray-900">{{ $t('settings.newMessageAlerts') }}</p>
                    <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.newMessageAlertsDesc') }}</p>
                  </div>
                  <Switch
                    :checked="notificationSettings.new_message_alerts"
                    @update:checked="notificationSettings.new_message_alerts = $event"
                  />
                </div>
                <Separator class="bg-white/[0.08] light:bg-gray-200" />
                <div class="flex items-center justify-between">
                  <div>
                    <p class="font-medium text-white light:text-gray-900">{{ $t('settings.campaignUpdates') }}</p>
                    <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.campaignUpdatesDesc') }}</p>
                  </div>
                  <Switch
                    :checked="notificationSettings.campaign_updates"
                    @update:checked="notificationSettings.campaign_updates = $event"
                  />
                </div>
                <div class="flex justify-end pt-4">
                  <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="saveNotificationSettings" :disabled="isSubmitting">
                    <Loader2 v-if="isSubmitting" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('settings.save') }}
                  </Button>
                </div>
              </div>
            </div>
            <div v-if="userID" class="mt-4">
              <AuditLogPanel :key="notificationLogKey" resource-type="settings.notification" :resource-id="userID" />
            </div>
          </TabsContent>

          <!-- Calling Settings Tab -->
          <TabsContent value="calling">
            <div class="rounded-xl border border-white/[0.08] bg-white/[0.02] light:bg-white light:border-gray-200">
              <div class="p-6 pb-3">
                <h3 class="text-lg font-semibold text-white light:text-gray-900">{{ $t('settings.callingSettings') }}</h3>
                <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.callingSettingsDesc') }}</p>
              </div>
              <div class="p-6 pt-3 space-y-4">
                <div class="flex items-center justify-between">
                  <div>
                    <p class="font-medium text-white light:text-gray-900">{{ $t('settings.callingEnabled') }}</p>
                    <p class="text-sm text-white/40 light:text-gray-500">{{ $t('settings.callingEnabledDesc') }}</p>
                  </div>
                  <Switch
                    :checked="callingSettings.calling_enabled"
                    @update:checked="callingSettings.calling_enabled = $event"
                  />
                </div>
                <Separator class="bg-white/[0.08] light:bg-gray-200" />
                <div class="grid grid-cols-2 gap-4" :class="{ 'opacity-50 pointer-events-none': !callingSettings.calling_enabled }">
                  <div class="space-y-2">
                    <Label for="max_call_duration" class="text-white/70 light:text-gray-700">{{ $t('settings.maxCallDuration') }}</Label>
                    <Input
                      id="max_call_duration"
                      type="number"
                      v-model.number="callingSettings.max_call_duration"
                      :min="60"
                      :max="3600"
                    />
                    <p class="text-xs text-white/40 light:text-gray-500">{{ $t('settings.maxCallDurationDesc') }}</p>
                  </div>
                  <div class="space-y-2">
                    <Label for="transfer_timeout" class="text-white/70 light:text-gray-700">{{ $t('settings.transferTimeout') }}</Label>
                    <Input
                      id="transfer_timeout"
                      type="number"
                      v-model.number="callingSettings.transfer_timeout_secs"
                      :min="30"
                      :max="600"
                    />
                    <p class="text-xs text-white/40 light:text-gray-500">{{ $t('settings.transferTimeoutDesc') }}</p>
                  </div>
                </div>
                <Separator class="bg-white/[0.08] light:bg-gray-200" />
                <!-- Hold Music Upload -->
                <div class="space-y-3" :class="{ 'opacity-50 pointer-events-none': !callingSettings.calling_enabled }">
                  <div>
                    <Label class="text-white/70 light:text-gray-700 flex items-center gap-2">
                      <Music class="h-4 w-4" />
                      {{ $t('settings.holdMusic') }}
                    </Label>
                    <p class="text-xs text-white/40 light:text-gray-500 mt-1">{{ $t('settings.holdMusicDesc') }}</p>
                  </div>
                  <div class="flex items-center gap-3">
                    <span class="text-sm text-white/50 light:text-gray-500">
                      {{ callingSettings.hold_music_file ? `${$t('settings.currentFile')}: ${callingSettings.hold_music_file}` : $t('settings.noFileUploaded') }}
                    </span>
                    <Button
                      v-if="callingSettings.hold_music_file"
                      variant="ghost"
                      size="sm"
                      class="h-8 w-8 p-0 text-white/50 hover:text-white light:text-gray-500 light:hover:text-gray-900"
                      @click="togglePlayAudio('hold_music')"
                    >
                      <Pause v-if="playingHoldMusic" class="h-4 w-4" />
                      <Play v-else class="h-4 w-4" />
                    </Button>
                  </div>
                  <div class="flex items-center gap-2">
                    <input ref="holdMusicInput" type="file" accept=".ogg,.opus,.mp3,.wav" class="hidden" @change="uploadAudio('hold_music', $event)" />
                    <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="holdMusicInput?.click()" :disabled="isUploadingHoldMusic">
                      <Loader2 v-if="isUploadingHoldMusic" class="mr-2 h-4 w-4 animate-spin" />
                      <Upload v-else class="mr-2 h-4 w-4" />
                      {{ $t('settings.uploadAudio') }}
                    </Button>
                    <span class="text-xs text-white/30 light:text-gray-400">.ogg, .opus, .mp3, .wav (max 5MB)</span>
                  </div>
                </div>
                <!-- Ringback Tone Upload -->
                <div class="space-y-3" :class="{ 'opacity-50 pointer-events-none': !callingSettings.calling_enabled }">
                  <div>
                    <Label class="text-white/70 light:text-gray-700 flex items-center gap-2">
                      <Phone class="h-4 w-4" />
                      {{ $t('settings.ringbackTone') }}
                    </Label>
                    <p class="text-xs text-white/40 light:text-gray-500 mt-1">{{ $t('settings.ringbackToneDesc') }}</p>
                  </div>
                  <div class="flex items-center gap-3">
                    <span class="text-sm text-white/50 light:text-gray-500">
                      {{ callingSettings.ringback_file ? `${$t('settings.currentFile')}: ${callingSettings.ringback_file}` : $t('settings.noFileUploaded') }}
                    </span>
                    <Button
                      v-if="callingSettings.ringback_file"
                      variant="ghost"
                      size="sm"
                      class="h-8 w-8 p-0 text-white/50 hover:text-white light:text-gray-500 light:hover:text-gray-900"
                      @click="togglePlayAudio('ringback')"
                    >
                      <Pause v-if="playingRingback" class="h-4 w-4" />
                      <Play v-else class="h-4 w-4" />
                    </Button>
                  </div>
                  <div class="flex items-center gap-2">
                    <input ref="ringbackInput" type="file" accept=".ogg,.opus,.mp3,.wav" class="hidden" @change="uploadAudio('ringback', $event)" />
                    <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="ringbackInput?.click()" :disabled="isUploadingRingback">
                      <Loader2 v-if="isUploadingRingback" class="mr-2 h-4 w-4 animate-spin" />
                      <Upload v-else class="mr-2 h-4 w-4" />
                      {{ $t('settings.uploadAudio') }}
                    </Button>
                    <span class="text-xs text-white/30 light:text-gray-400">.ogg, .opus, .mp3, .wav (max 5MB)</span>
                  </div>
                </div>
                <div class="flex justify-end pt-4">
                  <Button variant="outline" size="sm" class="bg-white/[0.04] border-white/[0.1] text-white/70 hover:bg-white/[0.08] hover:text-white light:bg-white light:border-gray-200 light:text-gray-700 light:hover:bg-gray-50" @click="saveCallingSettings" :disabled="isSubmitting">
                    <Loader2 v-if="isSubmitting" class="mr-2 h-4 w-4 animate-spin" />
                    {{ $t('settings.save') }}
                  </Button>
                </div>
              </div>
            </div>
            <div v-if="orgID" class="mt-4">
              <AuditLogPanel :key="callingLogKey" resource-type="settings.calling" :resource-id="orgID" />
            </div>
          </TabsContent>
        </div>
      </div>
    </ScrollArea>
      </template>

      <TabsContent v-if="canSee('units')" value="units" class="mt-0 min-h-0 flex-1"><UnitsView /></TabsContent>
      <TabsContent v-if="canSee('departments')" value="departments" class="mt-0 min-h-0 flex-1"><DepartmentsView /></TabsContent>
    </Tabs>
  </div>
</template>
