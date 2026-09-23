export interface SettingsTabConfig {
  value: string
  permission: string
}

export function resolveActiveTab(
  tabs: SettingsTabConfig[],
  requestedTab: string | undefined,
  defaultTab: string,
  hasPermission: (permission: string) => boolean,
): string | null {
  const accessible = tabs.filter(tab => hasPermission(tab.permission))
  if (accessible.length === 0) return null
  if (requestedTab && accessible.some(tab => tab.value === requestedTab)) return requestedTab
  if (accessible.some(tab => tab.value === defaultTab)) return defaultTab
  return accessible[0].value
}
