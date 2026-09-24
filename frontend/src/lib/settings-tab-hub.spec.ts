import { describe, it, expect } from 'vitest'
import { resolveActiveTab, type SettingsTabConfig } from './settings-tab-hub'

const tabs: SettingsTabConfig[] = [
  { value: 'stages', permission: 'occurrences.stages' },
  { value: 'categories', permission: 'occurrences.categories' },
  { value: 'processes', permission: 'occurrences.processes' },
]

describe('resolveActiveTab', () => {
  it('honours the requested tab when it is accessible', () => {
    const result = resolveActiveTab(tabs, 'processes', 'stages', () => true)
    expect(result).toBe('processes')
  })

  it('falls back to the default tab when no tab is requested', () => {
    const result = resolveActiveTab(tabs, undefined, 'stages', () => true)
    expect(result).toBe('stages')
  })

  it('falls back to the first accessible tab when the requested tab is not permitted', () => {
    const hasPermission = (p: string) => p === 'occurrences.categories'
    const result = resolveActiveTab(tabs, 'processes', 'stages', hasPermission)
    expect(result).toBe('categories')
  })

  it('falls back to the first accessible tab when the default tab is not permitted', () => {
    const hasPermission = (p: string) => p === 'occurrences.processes'
    const result = resolveActiveTab(tabs, undefined, 'stages', hasPermission)
    expect(result).toBe('processes')
  })

  it('lands a single-permission user on their only tab even when a different tab is requested', () => {
    const hasPermission = (p: string) => p === 'occurrences.categories'
    const result = resolveActiveTab(tabs, 'processes', 'stages', hasPermission)
    expect(result).toBe('categories')
  })

  it('treats an unknown requested value as not accessible', () => {
    const result = resolveActiveTab(tabs, 'not-a-real-tab', 'stages', () => true)
    expect(result).toBe('stages')
  })

  it('returns null when nothing is accessible', () => {
    const result = resolveActiveTab(tabs, undefined, 'stages', () => false)
    expect(result).toBeNull()
  })
})
