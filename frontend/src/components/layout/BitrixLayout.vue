<script setup lang="ts">
// "Bitrix" system theme (default): wallpaper, glass sidebar, top tabs for the
// active module's pages. AppLayout picks it from branding.ui_theme.
import { ref, computed, onMounted } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { Menu, X } from 'lucide-vue-next'
import { wsService } from '@/services/websocket'
import { authService } from '@/services/api'
import { useBranding } from '@/composables/useBranding'
import { useWallpaper } from '@/composables/useWallpaper'
import OrganizationSwitcher from './OrganizationSwitcher.vue'
import UserMenu from './UserMenu.vue'
import WallpaperPicker from './WallpaperPicker.vue'
import ActiveCallPanel from '@/components/calling/ActiveCallPanel.vue'
import { ScrollToTop } from '@/components/shared'
import { navigationSections, type NavSection, type NavItem } from './navigation'

useI18n() // Enable $t() in template

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const { logoUrl, systemName, loadBranding } = useBranding()
const { background: wallpaper } = useWallpaper()
const isCollapsed = ref(true)
const isMobileMenuOpen = ref(false)

// Refresh user data and connect WebSocket on mount
onMounted(() => {
  loadBranding()
  if (authStore.isAuthenticated) {
    // Fetch fresh permissions in background (non-destructive — interceptor handles 401)
    authStore.refreshUserData()

    wsService.connect(async () => {
      try {
        const resp = await authService.getWSToken()
        return resp.data.data.token
      } catch {
        return null
      }
    })
  }
})

function isAccessible(item: NavItem): boolean {
  if (item.childPermissions) {
    return item.childPermissions.some(p => authStore.hasPermission(p, 'read'))
  }
  return !item.permission || authStore.hasPermission(item.permission, item.permissionAction || 'read')
}

function filterItems(items: NavSection['items']) {
  return items
    .filter(isAccessible)
    .map(item => {
      const filteredChildren = item.children?.filter(isAccessible)
      const filteredGroups = item.groups
        ?.map(group => ({
          ...group,
          items: group.items.filter(isAccessible)
        }))
        .filter(group => group.items.length > 0)

      const firstAccessibleChild: NavItem | undefined = filteredChildren?.[0] ?? filteredGroups?.[0]?.items[0]

      let effectivePath = item.path
      if (item.childPermissions && item.permission && !authStore.hasPermission(item.permission, item.permissionAction || 'read') && firstAccessibleChild) {
        effectivePath = firstAccessibleChild.path
      }

      const originalPath = item.path
      const isActive = originalPath === '/'
        ? route.name === 'dashboard'
        : originalPath === '/chat'
          ? route.path === '/chat' || route.path.startsWith('/chat/')
          : route.path.startsWith(originalPath)

      return {
        ...item,
        path: effectivePath,
        active: isActive,
        children: filteredChildren,
        groups: filteredGroups
      }
    })
}

// Filter navigation sections based on user permissions
const navSections = computed(() => {
  return navigationSections
    .map(section => ({
      ...section,
      items: filterItems(section.items)
    }))
    .filter(section => section.items.length > 0)
})

// Bitrix-style top tabs: the active module's sub-pages live only here, so the
// sidebar stays a flat list of modules. Groups (Settings) become clusters
// split by a divider instead of labelled headers.
const activeItem = computed(() => navSections.value.flatMap(s => s.items).find(i => i.active))
const tabGroups = computed(() => {
  const item = activeItem.value
  if (item?.groups) return item.groups
  if (item?.children?.length) return [{ label: item.name, items: item.children }]
  return []
})
// The tab whose path is the longest prefix of the URL: /chat/<id> lights
// "Conversations", /chat/notes lights "Notes", /settings/sso lights "SSO".
const activeTabPath = computed(() => {
  const p = route.path
  return tabGroups.value
    .flatMap(g => g.items.map(i => i.path))
    .filter(tp => p === tp || p.startsWith(tp.endsWith('/') ? tp : tp + '/'))
    .sort((a, b) => b.length - a.length)[0]
})

// Unlabelled sections (Settings) are pinned to the sidebar's bottom edge.
const mainSections = computed(() => navSections.value.filter(s => s.label))
const footerItems = computed(() => navSections.value.filter(s => !s.label).flatMap(s => s.items))

const toggleSidebar = () => {
  isCollapsed.value = !isCollapsed.value
}

const handleLogout = async () => {
  await authStore.logout()
  router.push('/login')
}
</script>

<template>
  <div class="on-wallpaper relative flex h-screen overflow-hidden" :style="{ background: wallpaper, '--wallpaper': wallpaper }">
    <!-- Skip link for accessibility -->
    <a href="#main-content" class="skip-link">{{ $t('nav.skipToMain') }}</a>

    <!-- Mobile menu overlay -->
    <div
      v-if="isMobileMenuOpen"
      class="fixed inset-0 z-40 bg-black/50 backdrop-blur-sm md:hidden"
      @click="isMobileMenuOpen = false"
    />

    <!-- Sidebar: glass over the wallpaper, white text in both colour modes -->
    <aside
      :class="[
        'shell-glass flex shrink-0 flex-col text-white transition-[width,transform] duration-300',
        'fixed inset-y-0 left-0 z-40 bg-black/45 backdrop-blur-xl md:relative md:bg-black/15 md:backdrop-blur-md',
        isMobileMenuOpen ? 'translate-x-0' : '-translate-x-full md:translate-x-0',
        isCollapsed ? 'w-[272px] md:w-[70px]' : 'w-[272px]'
      ]"
      role="navigation"
      :aria-label="$t('nav.mainNavigation')"
    >
      <!-- Geometry is in px on purpose (html is 14px, so rem spacing lands on
           half pixels). Everything shares one icon axis: 10px nav gutter +
           4px group inset + 12px item padding = icon at 26px expanded, and
           centred at 35px in the 70px collapsed rail. The toggle (15px + 40px
           button, 18px icon) and the org switcher sit on that same axis. -->
      <div class="flex h-14 shrink-0 items-center gap-2 px-[15px]">
        <button
          type="button"
          data-testid="sidebar-toggle"
          class="hidden h-[40px] w-[40px] shrink-0 items-center justify-center rounded-xl text-white/85 transition hover:bg-white/15 hover:text-white md:flex"
          :aria-label="isCollapsed ? $t('nav.expandSidebar') : $t('nav.collapseSidebar')"
          :aria-expanded="!isCollapsed"
          @click="toggleSidebar"
        >
          <Menu class="h-[18px] w-[18px]" aria-hidden="true" />
        </button>
        <RouterLink
          to="/"
          :class="['flex min-w-0 items-center gap-2 text-white hover:text-white', isCollapsed && 'md:hidden']"
          @click="isMobileMenuOpen = false"
        >
          <img :src="logoUrl || '/logo-atacadao.png'" alt="" class="h-7 w-7 shrink-0 rounded-lg object-cover shadow-lg shadow-black/20">
          <span class="truncate text-[17px] font-semibold tracking-tight">{{ systemName || 'WhatsApp ATC' }}</span>
        </RouterLink>
      </div>

      <!-- Organization Switcher (Super Admin only) -->
      <OrganizationSwitcher :collapsed="isCollapsed" @expand="isCollapsed = false" />

      <!-- Navigation -->
      <ScrollArea class="min-h-0 flex-1">
        <nav class="space-y-[8px] px-[10px] pb-[8px]" role="menubar">
          <!-- Each labelled section is a frosted group, like Bitrix's "Collaboration" block -->
          <div
            v-for="section in mainSections"
            :key="section.label"
            class="rounded-2xl bg-white/[0.08] p-[4px]"
          >
            <div
              v-if="!isCollapsed"
              class="px-[12px] pb-[4px] pt-[6px] text-[11px] font-medium text-white/60"
            >
              {{ $t(section.label) }}
            </div>

            <div class="space-y-[2px]">
              <template v-for="item in section.items" :key="item.path">
                <!-- ponytail: tooltip still mounts once isCollapsed flips true even on
                     sub-md viewports where the label stays visible; needs viewport-width
                     tracking to fix, out of scope for this pass -->
                <Tooltip :disabled="!isCollapsed" :delay-duration="150">
                  <TooltipTrigger as-child>
                    <RouterLink
                      :to="item.path"
                      :class="[
                        'shell-nav-item btn-press',
                        item.active && 'is-active',
                        isCollapsed && 'md:justify-center md:px-0'
                      ]"
                      :data-active="item.active"
                      role="menuitem"
                      :aria-current="item.active ? 'page' : undefined"
                      @click="isMobileMenuOpen = false"
                    >
                      <component :is="item.icon" class="h-[18px] w-[18px] shrink-0" aria-hidden="true" />
                      <span :class="['truncate', isCollapsed && 'md:sr-only']">{{ $t(item.name) }}</span>
                    </RouterLink>
                  </TooltipTrigger>
                  <TooltipContent side="right">{{ $t(item.name) }}</TooltipContent>
                </Tooltip>
              </template>
            </div>
          </div>
        </nav>
      </ScrollArea>

      <!-- Pinned to the bottom (Bitrix keeps Settings there too). Same 10+4px
           inset as the groups above so the icon stays on the shared axis. -->
      <div v-if="footerItems.length" class="shrink-0 px-[14px] pb-[12px] pt-[6px]" role="menubar">
        <Tooltip v-for="item in footerItems" :key="item.path" :disabled="!isCollapsed" :delay-duration="150">
          <TooltipTrigger as-child>
            <RouterLink
              :to="item.path"
              :class="[
                'shell-nav-item btn-press',
                item.active && 'is-active',
                isCollapsed && 'md:justify-center md:px-0'
              ]"
              :data-active="item.active"
              role="menuitem"
              :aria-current="item.active ? 'page' : undefined"
              @click="isMobileMenuOpen = false"
            >
              <component :is="item.icon" class="h-[18px] w-[18px] shrink-0" aria-hidden="true" />
              <span :class="['truncate', isCollapsed && 'md:sr-only']">{{ $t(item.name) }}</span>
            </RouterLink>
          </TooltipTrigger>
          <TooltipContent side="right">{{ $t(item.name) }}</TooltipContent>
        </Tooltip>
      </div>
    </aside>

    <div class="flex min-w-0 flex-1 flex-col">
      <!-- Top bar: module tabs on the left, workspace tools on the right -->
      <header class="shell-glass flex h-14 shrink-0 items-center gap-2 px-2 text-white md:px-3">
        <button
          type="button"
          class="shell-chip h-9 w-9 justify-center md:hidden"
          :aria-label="$t('nav.toggleMenu')"
          :aria-expanded="isMobileMenuOpen"
          @click="isMobileMenuOpen = !isMobileMenuOpen"
        >
          <X v-if="isMobileMenuOpen" class="h-5 w-5" />
          <Menu v-else class="h-5 w-5" />
        </button>

        <nav
          class="no-scrollbar flex min-w-0 flex-1 items-center gap-1 overflow-x-auto"
          :aria-label="activeItem ? $t(activeItem.name) : undefined"
        >
          <template v-if="tabGroups.length">
            <template v-for="(group, gIdx) in tabGroups" :key="group.label">
              <span v-if="gIdx > 0" class="mx-1.5 h-5 w-px shrink-0 bg-white/25" aria-hidden="true" />
              <div class="flex shrink-0 items-center gap-1" role="group" :aria-label="$t(group.label)">
                <RouterLink
                  v-for="tab in group.items"
                  :key="tab.path"
                  :to="tab.path"
                  :class="['shell-tab', activeTabPath === tab.path && 'is-active']"
                  :aria-current="activeTabPath === tab.path ? 'page' : undefined"
                >
                  {{ $t(tab.name) }}
                </RouterLink>
              </div>
            </template>
          </template>
          <span v-else-if="activeItem" class="shell-tab is-active">{{ $t(activeItem.name) }}</span>
        </nav>

        <WallpaperPicker />
        <UserMenu collapsed @logout="handleLogout" />
      </header>

      <!-- Main content: a rounded sheet floating over the wallpaper -->
      <main
        id="main-content"
        class="relative mx-1.5 mb-1.5 flex-1 overflow-hidden rounded-2xl bg-background shadow-[0_8px_40px_-12px_rgba(0,0,0,0.45)] md:mb-2 md:ml-0 md:mr-2"
        role="main"
      >
        <RouterView v-slot="{ Component, route: viewRoute }">
          <Transition name="page">
            <component :is="Component" :key="viewRoute.meta.stableKey ? String(viewRoute.name) : viewRoute.path" />
          </Transition>
        </RouterView>
        <ActiveCallPanel />
        <ScrollToTop />
      </main>
    </div>
  </div>
</template>
