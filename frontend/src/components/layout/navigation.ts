import {
  LayoutDashboard,
  MessageSquare,
  Bot,
  FileText,
  Megaphone,
  Settings,
  Users,
  Workflow,
  Sparkles,
  Key,
  UserX,
  BarChart3,
  ShieldCheck,
  LineChart,
  PhoneCall,
  PhoneForwarded,
  ScrollText,
  ClipboardList
} from 'lucide-vue-next'
import type { Component } from 'vue'

export interface NavItem {
  name: string
  path: string
  icon: Component
  permission?: string
  childPermissions?: string[]
  children?: NavItem[]
  /** Grouped submenu (labelled clusters), used instead of `children` when a
   *  flat list would get too long to scan (e.g. Settings). */
  groups?: NavChildGroup[]
}

export interface NavChildGroup {
  label: string
  items: NavItem[]
}

export interface NavSection {
  label: string
  items: NavItem[]
  /** Pin to bottom of sidebar */
  pinBottom?: boolean
}

export const navigationSections: NavSection[] = [
  {
    label: 'nav.sectionMain',
    items: [
      {
        name: 'nav.dashboard',
        path: '/',
        icon: LayoutDashboard,
        permission: 'analytics'
      },
      {
        name: 'nav.chat',
        path: '/chat',
        icon: MessageSquare,
        permission: 'chat'
      },
      {
        name: 'nav.crm',
        path: '/crm/occurrences',
        icon: ClipboardList,
        permission: 'occurrences'
      },
    ]
  },
  {
    label: 'nav.sectionMessaging',
    items: [
      {
        name: 'nav.chatbot',
        path: '/chatbot',
        icon: Bot,
        permission: 'settings.chatbot',
        childPermissions: ['settings.chatbot', 'chatbot.keywords', 'flows.chatbot', 'chatbot.ai', 'transfers'],
        children: [
          { name: 'nav.overview', path: '/chatbot', icon: Bot, permission: 'settings.chatbot' },
          { name: 'nav.keywords', path: '/chatbot/keywords', icon: Key, permission: 'chatbot.keywords' },
          { name: 'nav.flows', path: '/chatbot/flows', icon: Workflow, permission: 'flows.chatbot' },
          { name: 'nav.aiContexts', path: '/chatbot/ai', icon: Sparkles, permission: 'chatbot.ai' },
          { name: 'nav.transfers', path: '/chatbot/transfers', icon: UserX, permission: 'transfers' }
        ]
      },
      {
        name: 'nav.campaigns',
        path: '/campaigns',
        icon: Megaphone,
        permission: 'campaigns'
      },
      {
        name: 'nav.templates',
        path: '/templates',
        icon: FileText,
        permission: 'templates'
      },
      {
        name: 'nav.flows',
        path: '/flows',
        icon: Workflow,
        permission: 'flows.whatsapp'
      },
    ]
  },
  {
    label: 'nav.sectionCalling',
    items: [
      { name: 'nav.callLogs', path: '/calling/logs', icon: PhoneCall, permission: 'call_logs' },
      { name: 'nav.ivrFlows', path: '/calling/ivr-flows', icon: Workflow, permission: 'ivr_flows' },
      { name: 'nav.callTransfers', path: '/calling/transfers', icon: PhoneForwarded, permission: 'call_transfers' },
    ]
  },
  {
    label: 'nav.sectionAnalytics',
    items: [
      {
        name: 'nav.agentAnalytics',
        path: '/analytics/agents',
        icon: BarChart3,
        permission: 'analytics.agents'
      },
      {
        name: 'nav.metaInsights',
        path: '/analytics/meta-insights',
        icon: LineChart,
        permission: 'analytics'
      },
    ]
  },
  {
    label: '',
    pinBottom: true,
    items: [
      {
        name: 'nav.settings',
        path: '/settings',
        icon: Settings,
        permission: 'settings.general',
        childPermissions: ['settings.general', 'settings.chatbot', 'accounts', 'contacts', 'canned_responses', 'tags', 'teams', 'users', 'roles', 'api_keys', 'webhooks', 'custom_actions', 'occurrences.stages', 'occurrences.categories', 'occurrences.what_happened', 'occurrences.processes', 'occurrences.sla_policies', 'units', 'settings.sso', 'audit_logs'],
        groups: [
          {
            label: 'nav.groupOrganization',
            items: [
              { name: 'nav.general', path: '/settings', icon: Settings, permission: 'settings.general' },
              { name: 'nav.sso', path: '/settings/sso', icon: ShieldCheck, permission: 'settings.sso' },
              { name: 'nav.auditLogs', path: '/settings/audit-logs', icon: ScrollText, permission: 'audit_logs' }
            ]
          },
          {
            label: 'nav.groupChannels',
            items: [
              { name: 'nav.accounts', path: '/settings/accounts', icon: Users, permission: 'accounts' }
            ]
          },
          {
            label: 'nav.groupService',
            items: [
              { name: 'nav.groupService', path: '/settings/service', icon: Bot, childPermissions: ['settings.chatbot', 'canned_responses', 'tags', 'contacts'] }
            ]
          },
          {
            label: 'nav.groupOccurrences',
            items: [
              { name: 'nav.groupOccurrences', path: '/settings/occurrences', icon: ClipboardList, childPermissions: ['occurrences.stages', 'occurrences.categories', 'occurrences.what_happened', 'occurrences.processes', 'occurrences.sla_policies', 'units'] }
            ]
          },
          {
            label: 'nav.groupAccess',
            items: [
              { name: 'nav.groupAccess', path: '/settings/access', icon: Users, childPermissions: ['teams', 'users', 'roles'] }
            ]
          },
          {
            label: 'nav.groupIntegrations',
            items: [
              { name: 'nav.groupIntegrations', path: '/settings/integrations', icon: Key, childPermissions: ['api_keys', 'webhooks', 'custom_actions'] }
            ]
          }
        ]
      }
    ]
  }
]

// Flat list for backward compatibility (used by AppLayout computed)
export const navigationItems: NavItem[] = navigationSections.flatMap(s => s.items)
