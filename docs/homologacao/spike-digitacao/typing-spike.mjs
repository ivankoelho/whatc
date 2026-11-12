#!/usr/bin/env node
// SPIKE, NOT PRODUCT CODE. Answers, with evidence, how the WhatsApp Cloud API typing indicator behaves
// on a real TEST number, before anything is built into the product.
//
// Runs on your machine with YOUR test-number credentials, read from environment variables. They are never
// written to the evidence file, and nothing here is wired into the WhatC server.
//
//   WA_TOKEN      access token of the TEST number's app (never a production number)
//   WA_PHONE_ID   phone number id of the TEST number
//   WA_API_VERSION   optional, default v24.0
//   WA_BASE_URL      optional, default https://graph.facebook.com (a local simulator for a dry run)
//
//   node typing-spike.mjs --wamid <id of a message the phone SENT to the test number> [--wamid2 <a second one>] [--old-wamid <one older than 24h>] [--out evidence/typing-YYYYMMDD.json]
//
// Where to get a wamid: send a message from the phone to the test number, then read it from the WhatC
// database (see evidence.sql) or from the webhook payload (entry[].changes[].value.messages[].id).
//
// The script asks you, at each step, to LOOK AT THE PHONE and type what you saw. The evidence file holds
// the exact requests (token redacted), the HTTP answers, local timestamps and your observations.

import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { dirname } from 'node:path'
import { createInterface } from 'node:readline/promises'
import { stdin, stdout, argv, env, exit } from 'node:process'

const arg = (name) => { const i = argv.indexOf(name); return i >= 0 ? argv[i + 1] : undefined }
const TOKEN = env.WA_TOKEN
const PHONE_ID = env.WA_PHONE_ID
const VERSION = env.WA_API_VERSION || 'v24.0'
const BASE = (env.WA_BASE_URL || 'https://graph.facebook.com').replace(/\/$/, '')
const wamid = arg('--wamid')
const wamid2 = arg('--wamid2')
const oldWamid = arg('--old-wamid')
const out = arg('--out') || `evidence/typing-${new Date().toISOString().slice(0, 10)}.json`
const dryRun = BASE !== 'https://graph.facebook.com'

if (!TOKEN || !PHONE_ID || !wamid) {
  console.error('Missing WA_TOKEN, WA_PHONE_ID or --wamid. See the header of this file.')
  exit(2)
}
if (!dryRun && /prod/i.test(env.WA_ENVIRONMENT || '')) {
  console.error('Refusing to run against a production number.')
  exit(2)
}

const rl = createInterface({ input: stdin, output: stdout })
const evidence = { startedAt: new Date().toISOString(), baseUrl: BASE, apiVersion: VERSION, dryRun, steps: [] }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const ask = async (q) => {
  try {
    return (await rl.question(`\n  ? ${q}\n  > `)).trim()
  } catch {
    return '(no input: stdin closed)' // only a dry run piped from a file; a person always answers
  }
}
const save = () => { mkdirSync(dirname(out), { recursive: true }); writeFileSync(out, JSON.stringify(evidence, null, 2)) }

async function call(label, body) {
  const url = `${BASE}/${VERSION}/${PHONE_ID}/messages`
  const t0 = Date.now()
  let status = 0, text = ''
  try {
    const r = await fetch(url, { method: 'POST', headers: { Authorization: `Bearer ${TOKEN}`, 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
    status = r.status
    text = await r.text()
  } catch (e) {
    text = `network error: ${e.message}`
  }
  let parsed
  try { parsed = JSON.parse(text) } catch { parsed = text }
  const step = { label, at: new Date(t0).toISOString(), request: { url: url.replace(PHONE_ID, '<PHONE_ID>'), body }, status, response: parsed, ms: Date.now() - t0 }
  evidence.steps.push(step)
  save() // keep partial evidence if the run is interrupted
  console.log(`  [${label}] HTTP ${status} ${typeof parsed === 'string' ? parsed.slice(0, 160) : JSON.stringify(parsed).slice(0, 200)}`)
  return step
}

const typing = (id) => ({ messaging_product: 'whatsapp', status: 'read', message_id: id, typing_indicator: { type: 'text' } })
const textMessage = (to, body) => ({ messaging_product: 'whatsapp', recipient_type: 'individual', to, type: 'text', text: { body } })
const note = async (label, question) => {
  const answer = await ask(question)
  evidence.steps.push({ label, at: new Date().toISOString(), observation: answer })
  save()
}

async function main() {
  console.log(`\nTyping-indicator spike  (${dryRun ? 'DRY RUN against ' + BASE : 'REAL Graph API'})`)
  console.log('Use ONLY a test number. Keep the phone in your hand, with the conversation open.\n')
  const to = await ask('Phone number of the test handset, digits only (country code first), e.g. 5511999990000. Needed only for step 4 and 6.')

  // 1. does the call work, and how long does the indicator stay?
  console.log('\n== Step 1: show the indicator and time it. Watch the phone.')
  const s1 = await call('1-show', typing(wamid))
  await note('1-observed-start', 'Did "typing…" appear on the phone? (yes/no, and after how many seconds)')
  console.log('  Now do NOT send anything. Watch the phone until the indicator disappears.')
  await note('1-observed-duration', 'How many seconds until "typing…" disappeared by itself? (the docs say about 25)')
  evidence.steps.push({ label: '1-summary', accepted: s1.status === 200 && s1.response?.success === true })

  // 2. can it be renewed while the reply is being prepared?
  console.log('\n== Step 2: renewal. The script re-sends at 0 s, 10 s, 20 s and 30 s. Watch whether it stays on and for how long.')
  for (const waitMs of [0, 10000, 10000, 10000]) {
    await sleep(waitMs)
    await call(`2-renew+${evidence.steps.filter((s) => s.label?.startsWith('2-renew')).length * 10}s`, typing(wamid))
  }
  await note('2-observed', 'Did it stay visible past 25 s while you re-sent? Did a re-send restart the 25 s? Any error or flicker?')

  // 3. what happens when the customer writes more while it is showing?
  console.log('\n== Step 3: more messages while it shows.')
  await call('3-show', typing(wamid))
  console.log('  >>> NOW, on the phone, send the test number another message (any text), within the next 10 seconds.')
  await note('3-observed', 'What happened to "typing…" when you sent the new message? (stayed / disappeared / flickered)')
  if (wamid2) {
    await call('3-show-on-newer-message', typing(wamid2))
    await note('3-observed-newer', 'Did the indicator show for the newer message id? Any difference from the older id?')
  } else {
    evidence.steps.push({ label: '3-newer-id', skipped: 'no --wamid2 given' })
  }

  // 4. is it dismissed by the reply, and does the reply cost anything extra?
  console.log('\n== Step 4: the reply dismisses it.')
  if (to) {
    await call('4-show', typing(wamid))
    await sleep(5000)
    await call('4-reply', textMessage(to, 'Teste do spike: resposta enviada 5 s depois do indicador.'))
    await note('4-observed', 'Did the indicator disappear when the reply arrived? Did the reply look normal (no extra label)?')
  } else {
    evidence.steps.push({ label: '4-skipped', skipped: 'no handset number given' })
  }

  // 5. limits: wrong kind of id, and a message older than the window
  console.log('\n== Step 5: limits.')
  await call('5-garbage-id', typing('wamid.NOT-A-REAL-ID'))
  if (oldWamid) {
    await call('5-message-older-than-24h', typing(oldWamid))
    await note('5-observed-old', 'Did anything show on the phone for the OLD message id? (the docs do not state the 24 h window)')
  } else {
    evidence.steps.push({ label: '5-old-id', skipped: 'no --old-wamid given: the 24 h window stays unconfirmed' })
  }

  // 6. other types
  console.log('\n== Step 6: message types. The docs show only "text".')
  await call('6-type-audio', { ...typing(wamid), typing_indicator: { type: 'audio' } })
  await note('6-observed', 'Any visible effect for type "audio"? (the API answer above says whether it is accepted)')

  // 7. billing
  console.log('\n== Step 7: billing. A typing/read call returns no message id, so it cannot carry a price by itself.')
  console.log('  Wait a few minutes for the status webhooks of the reply sent in step 4 (if any), then:')
  await note('7-pricing', 'In the status webhooks (or the WhatC message_usage rows, see evidence.sql) of the reply, what category/billable did Meta report? Does the Meta Business Manager show any extra charge line for the typing calls?')

  evidence.finishedAt = new Date().toISOString()
  save()
  console.log(`\nEvidence written to ${out}. The token was never written. Send me this file.`)
  rl.close()
}

main().catch((e) => { console.error(e); exit(1) })
