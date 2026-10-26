import {
  PlusCircle, StickyNote, ArrowRightLeft, UserPlus, Send, CheckCircle2, Reply, Lightbulb, ClipboardList, Paperclip, MessageCircle,
} from 'lucide-vue-next'
import type { Component } from 'vue'

// How a case-history event is drawn and named: one place for the case screen and the full history.
export const eventIcons: Record<string, Component> = {
  opened: PlusCircle,
  note: StickyNote,
  stage_change: ArrowRightLeft,
  assignment: UserPlus,
  protocol_sent: Send,
  closed: CheckCircle2,
  reply: Reply,
  process_message_used: Lightbulb,
  forwarded: Send,
  participant_added: UserPlus,
  participant_removed: UserPlus,
  reopened: ArrowRightLeft,
  scope_regularized: ClipboardList,
  customer_notification: MessageCircle,
  file_added: Paperclip,
  file_removed: Paperclip,
}

export const eventLabels: Record<string, string> = {
  opened: 'occurrences.eventOpened',
  note: 'occurrences.eventNote',
  stage_change: 'occurrences.eventStageChange',
  assignment: 'occurrences.eventAssignment',
  protocol_sent: 'occurrences.eventProtocolSent',
  closed: 'occurrences.eventClosed',
  reply: 'occurrences.eventReply',
  process_message_used: 'occurrences.eventProcessMessageUsed',
  forwarded: 'occurrences.eventForwarded',
  participant_added: 'occurrences.eventParticipantAdded',
  participant_removed: 'occurrences.eventParticipantRemoved',
  reopened: 'occurrences.eventReopened',
  file_added: 'occurrences.eventFileAdded',
  file_removed: 'occurrences.eventFileRemoved',
  scope_regularized: 'occurrences.eventScopeRegularized',
  customer_notification: 'occurrences.eventCustomerNotice',
}

/** The types offered as filters, in the order people think about a case. */
export const historyFilterTypes = [
  'opened', 'forwarded', 'assignment', 'stage_change', 'closed', 'reopened', 'note', 'reply',
  'customer_notification', 'participant_added', 'participant_removed', 'file_added', 'file_removed', 'protocol_sent',
] as const
