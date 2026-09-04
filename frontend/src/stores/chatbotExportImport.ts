import { defineStore } from 'pinia'
import { ref } from 'vue'
import { chatbotService } from '@/services/api'

/** Portable export envelope produced by the backend `/chatbot/export` endpoint. */
export interface ChatbotFlowExport {
  version: number
  exported_at: string
  flow: {
    name: string
    [key: string]: any
  }
  keyword_rules: any[]
}

/** Result returned by the backend `/chatbot/import` endpoint. */
export interface ChatbotImportResult {
  imported_flow_id: string
  imported_flow_name: string
  imported_keywords: number
}

// The axios interceptor does not unwrap the response, and the backend wraps
// payloads in a `{ data: ... }` envelope. Read `.data.data` (falling back to
// `.data` for safety) so callers get the actual payload.
function unwrap<T>(response: { data: any }): T {
  return (response.data?.data ?? response.data) as T
}

function slugify(name: string): string {
  return (
    name
      .toLowerCase()
      .normalize('NFD')
      .replace(/[̀-ͯ]/g, '')
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/(^-|-$)/g, '')
      .slice(0, 60) || 'flow'
  )
}

function triggerDownload(filename: string, contents: string) {
  const blob = new Blob([contents], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  URL.revokeObjectURL(url)
}

export const useChatbotExportImportStore = defineStore('chatbotExportImport', () => {
  const exporting = ref(false)
  const importing = ref(false)
  const error = ref<string | null>(null)

  /**
   * Export a flow (graph + associated keyword rules) and trigger a download of
   * the portable JSON file. Returns the parsed export envelope.
   */
  async function exportFlow(flowId: string): Promise<ChatbotFlowExport> {
    exporting.value = true
    error.value = null
    try {
      const response = await chatbotService.exportFlow(flowId)
      const envelope = unwrap<ChatbotFlowExport>(response)
      const name = envelope?.flow?.name || 'flow'
      triggerDownload(`chatbot-flow-${slugify(name)}.json`, JSON.stringify(envelope, null, 2))
      return envelope
    } finally {
      exporting.value = false
    }
  }

  /**
   * Import a flow from the raw text of an exported file. The file is read as
   * text by the caller (`await file.text()`) and parsed here — the File object
   * is never sent to the backend. `whatsappAccount` is the target account the
   * imported flow/rules are attached to.
   */
  async function importFlow(fileText: string, whatsappAccount: string): Promise<ChatbotImportResult> {
    importing.value = true
    error.value = null
    try {
      let parsed: any
      try {
        parsed = JSON.parse(fileText)
      } catch {
        throw new Error('invalid-json')
      }

      const payload = { ...parsed, whatsapp_account: whatsappAccount }
      const response = await chatbotService.importFlow(payload)

      // Destructure the unwrapped payload (not the raw axios response).
      const { imported_flow_id, imported_flow_name, imported_keywords } =
        unwrap<ChatbotImportResult>(response)

      return { imported_flow_id, imported_flow_name, imported_keywords }
    } finally {
      importing.value = false
    }
  }

  return { exporting, importing, error, exportFlow, importFlow }
})
