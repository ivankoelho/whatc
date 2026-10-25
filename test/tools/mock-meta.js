// Simulated WhatsApp Cloud API for local validation. Nothing here reaches Meta.
//   POST /<version>/<phone_id>/messages  -> records the request, answers per mode
//   GET  /__mode?m=ok|400|429|500|drop   -> changes the behaviour
//   GET  /__reqs                          -> what was received
//   GET  /__clear
const http = require('http')
let mode = 'ok'
let seq = 0
const reqs = []
const pfx = 'MOCK' + Date.now().toString(36)
http.createServer((req, res) => {
  const url = new URL(req.url, 'http://x')
  if (url.pathname === '/__mode') { mode = url.searchParams.get('m') || 'ok'; return res.end(JSON.stringify({ mode })) }
  if (url.pathname === '/__reqs') { res.setHeader('Content-Type', 'application/json'); return res.end(JSON.stringify(reqs)) }
  if (url.pathname === '/__clear') { reqs.length = 0; return res.end('cleared') }
  let body = ''
  req.on('data', d => (body += d))
  req.on('end', () => {
    let j = {}
    try { j = JSON.parse(body) } catch {}
    const params = ((((j.template || {}).components || [])[0] || {}).parameters || []).map(p => p.text)
    const id = `wamid.${pfx}-${++seq}`
    reqs.push({ at: new Date().toISOString(), path: url.pathname, to: j.to, template: (j.template || {}).name, lang: ((j.template || {}).language || {}).code, params, mode, id })
    if (mode === 'drop') return req.socket.destroy()
    const send = (code, o) => { res.statusCode = code; res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(o)) }
    if (mode === '400') return send(400, { error: { message: 'Invalid parameter', code: 100 } })
    if (mode === '429') return send(429, { error: { message: 'Too many messages', code: 130429 } })
    if (mode === '500') return send(500, { error: { message: 'Internal error', code: 2 } })
    send(200, { messaging_product: 'whatsapp', contacts: [{ input: j.to, wa_id: j.to }], messages: [{ id }] })
  })
}).listen(9100, () => console.log('mock meta on :9100'))
