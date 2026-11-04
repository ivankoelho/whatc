# Protocol send: idempotency and state contract

Endpoints (all need `occurrences:write`, `chat:write` and a valid tie to the case/channel; another organization or case is a 404):

- `POST /api/occurrences/{id}/send-protocol`: header `Idempotency-Key: <uuid>` is **required** (400 `idempotency_key_required` / `idempotency_key_invalid`). Body `{ "message"?: string }`.
- `GET /api/occurrences/{id}/protocol-sends/{send_id}`: state of one send.
- `GET /api/occurrences/{id}/protocol-sends?idempotency_key=<uuid>`: the same, for a caller that never saw the answer and does not know the `send_id`. It never claims the key and never sends. It is not strictly a pure read: asking about a stale `processing` claim (older than 2 minutes) reconciles it from the evidence (moves the state forward, never sends). 404 = the server holds no claim for the key (the request never arrived or is still in flight): it is **not** proof that nothing was sent until a request in flight has ended (the server gives up on the provider after 30 s).

- `GET /api/occurrences/{id}/protocol-sends?open=true`: the case's **open send** (state `processing`, or `uncertain` until a person settles it), whoever started it and from whichever device. 404 `send_not_found` = none. Same authorization as sending; `own` says whether the caller started it, never who did.
- `POST /api/occurrences/{id}/protocol-sends/{send_id}/resolve`: a person settles an `uncertain` send (below). It needs the permission `occurrences:resolve_send`, **not** `occurrences:write` or `chat:write`, plus the scope to see the case.

## Answers

| result | HTTP | `sent` | `confirmed` | meaning |
|---|---|---|---|---|
| `sent` | 200 | true | true | the provider accepted the message |
| `failed` | 502 (422 closed window, 400/500 other proven refusals) | false | true | proven NOT sent |
| `uncertain` | 202 | false | false | the provider may have received it; do not send again, check the conversation |
| `processing` | 202 | false | false | the same key is still being worked on; nothing new was sent |

**`confirmed` means the outcome of the operation is known. It never means the customer received or read the message** (delivery is reported separately by the provider's status webhooks).

Every answer carries `send_id` and `message_id`. A repeat of the same request answers with the stored result and the header `Idempotent-Replay: true`; the same key with a different text, destination, case or person is a 409 `idempotency_key_reused`. A new intention to send uses a **new** key.

## Retention and expiry

- `sent` and `failed` records are kept 90 days after they were **settled** (the provider's answer, the reconciliation or a person's decision). `processing` and `uncertain` records are **never** removed: an `uncertain` record is what keeps the case protected until somebody settles it.
- **After the record is removed the key is unknown**: repeating the same key later is read as a new intention and **sends again**. Keys are single-use by contract; the interface never reuses the key of a finished intention, and the browser drops its own record long before 90 days (24 h).
- A claim stuck in `processing` for more than 2 minutes is reconciled from the audit trail and the message row. With insufficient evidence it becomes `uncertain`; the key is never released and nothing is resent. A missing local record is never read as proof that the provider did not receive the request.

## How a send is classified (`failed` vs `uncertain`)

The state follows **where the send stopped and what the provider proved**, never the HTTP status alone. `failed` is only for a send that is proven NOT delivered; anything else that touched the provider is `uncertain`, the key stays taken and nothing is ever resent automatically.

| where it stopped | evidence | result |
|---|---|---|
| validation (window closed, no account, bad body) | nothing reached the provider | `failed` |
| the attempt record (audit) cannot be written | nothing reached the provider | `failed` ("nothing was sent") |
| the message row cannot be created | nothing reached the provider | `failed` |
| connection never established | no byte was written (`dial` error) | `failed` |
| provider answers 4xx with a documented refusal code (100, 190, 131047, ...) | the provider refused it | `failed` |
| provider answers with a documented throttle code (130429, 131056, 80007) | refused, retry later | `failed` |
| 4xx with a code Meta does not document as a refusal, 4xx without a Meta body, 408/425 | proves nothing | `uncertain` |
| 5xx, proxy page, reset, timeout after the call started | the provider may have accepted it | `uncertain` |
| provider accepted, but the audit **result** row cannot be written | the answer follows the provider, not the audit | `sent` / `uncertain` / `failed` as the provider said |
| provider accepted, but the intent row cannot be closed | stays `processing`; reconciled from the message row (provider accepted -> `sent`) or, with no evidence, `uncertain` | never `failed` |
| the process dies mid-send | stale `processing` after 2 minutes, reconciled by evidence | `sent` or `uncertain`, never `failed` |

A `failed` row of the local message (the HTTP client gave up) is **not** evidence that the provider did not receive the request.

## One open send per case

A case has at most **one open customer send**: a send in `processing`, or `uncertain` until it is settled. While one exists, **nobody** can start another from that case: not another person, not another device or tab, not the same person with a new key or an edited text. Other cases (even of the same customer) and other organizations are not affected. `sent` and `failed` sends hold nothing.

- **The database enforces it**, not a check before the send: the partial unique index `uq_send_intent_open` on `(organization_id, occurrence_id, kind) WHERE state IN ('processing','uncertain')`. The claim is one `INSERT … ON CONFLICT DO NOTHING` (no conflict target), so the key index and this index are both enforced atomically, whatever the number of requests, users or servers.
- **Order of the answers.** The same key is answered first, from its own record (replay, `409 idempotency_key_reused`, `202 processing`). Only a different key that finds the case taken gets `409 send_pending`. A stale `processing` claim that is in the way is reconciled from the evidence first (it may have ended).
- **`409 send_pending`**: `error_type: "send_pending"`, `data: { result: "pending", sent: false, confirmed: false, send_id, state, own, claimed_at }`. It tells the page what to ask about and whether the caller started it. It never carries who started it, the text, or anything of another organization. Nothing is sent and no record is left for the refused request. It is a different answer from `idempotency_key_reused` and from the `202 processing` of the same key.
- **`uncertain` is never released by time.** Not by the reconciler, not by the retention, not after 72 hours or ten years. It leaves only by (a) the provider's own confirmation (a message id and an advanced status), which makes it `sent`, or (b) a person's decision.

### Settling an uncertain send (`resolve`)

A person who checked the conversation with the customer says what they found. It sends **nothing** and never resends.

- Body: `{ "outcome": "delivered" | "not_delivered", "conversation_checked": true, "note": "<what the conversation showed, 10 to 1000 characters>" }`. Anything else is 400 `resolution_invalid`.
- Authorization: `occurrences:resolve_send` **and** the scope to see the case (a supervisor settles only what is under their management; another organization is 404). The backend checks both; hiding the button is not the control.
- Only an `uncertain` send can be settled (409 `send_not_uncertain` otherwise, also when somebody settled it first). The provider's own evidence wins: if the records show the provider accepted it, the send is settled as `sent` first and the decision is refused.
- Effect: `delivered` → state `sent`; `not_delivered` → state `failed`; the case is free for a new send. The intent records who, when, the outcome and the note; the decision, the new state and the audit entry are **one transaction** (if the audit entry cannot be written nothing changes).
- Permanent audit entry (`result: resolved_delivered | resolved_not_delivered`, with outcome, note, `conversation_checked`, previous state, send and message ids). It is final through the interface; a later correction is a new audited action, not an edit.
- `not_delivered` frees the case: if the message was in fact delivered the customer receives the next one as a duplicate. The interface says so before it is chosen.
- The state query then reports `resolved_by_person: true`, the outcome and the time, never who.
- Default grants: system roles `admin` and `manager`, and the presets `Supervisor de Loja` and `Supervisor de SAC`. SAC, Vendas, Logística, Jurídico and agent roles do not have it. The permission name is `occurrences:resolve_send` (the action column of `permissions` is `varchar(20)`).

### Reconciliation

Two separate queries, so an old `uncertain` can never keep a newer stale `processing` waiting: (1) stale `processing` claims (older than 2 minutes), oldest first, in batches of 200; (2) unsettled `uncertain` ones **finished within the last 72 hours**, newest first, batch of 200. The 72 hours only bound how long the background loop keeps re-reading an uncertain send (provider confirmations arrive within hours); they never bound the protection: after them the send stays open, the case stays blocked, the record is kept, the state query still upgrades it if a confirmation arrives later, and a person can still settle it. Reconciliation never sends, never frees a key and never turns `uncertain` into `failed`.

## Deployment: migration, order and rollback

**Required migration.** The table `occurrence_send_intents` is created by GORM AutoMigrate (`internal/database/postgres.go`), and AutoMigrate only runs when the backend starts with the `-migrate` flag (`whatomate server -migrate`). **Without it the table does not exist and the protocol send does not work** (every `send-protocol` fails; the recovery endpoints fail too). The migration is additive: it creates a new table and its indexes, and adds one permission row; no existing table schema is changed (the permission name fits the existing `permissions.action varchar(20)`) and no existing row is modified, except that `-migrate` also grants `occurrences:resolve_send` to the roles listed above (additive, once per organization, never overruling an administrator who removed it).

The one-open-send index `uq_send_intent_open` is created by the migration code (`EnsureOpenSendIntentUniqueness`), not by AutoMigrate. **If the table already holds a case with more than one open send, the migration STOPS with an error that lists each case (organization, case, the send ids, dates); it creates nothing and changes nothing, and it never deletes or edits a row.** The operator settles each listed send after checking the conversation (the resolve endpoint, or by hand), then runs the migration again. A new installation, and any production database that has never run this feature, cannot have such rows. The read-only diagnostic is `database.OpenSendIntentDuplicatesSQL`.

**Order.**
1. Back up the database.
2. Start the new backend **once with `-migrate`** (with the new binary), before the new version is made available to users, and wait for the boot to finish.
3. Verify the table and the indexes (below). Only then release the flow.
4. Deploy the new frontend **together with** the new backend. The backend now requires `Idempotency-Key` on `send-protocol` (400 `idempotency_key_required`), so an old frontend cannot send protocols against the new backend, and the new frontend depends on the recovery endpoints of the new backend. Do not leave one side on the old version.

**Verification before releasing the flow** (read-only):

```sql
-- the table exists
SELECT to_regclass('public.occurrence_send_intents');            -- must not be NULL
-- the UNIQUE index that makes "one key, one send" true in the database
SELECT indexdef FROM pg_indexes
 WHERE tablename = 'occurrence_send_intents' AND indexname = 'uq_send_intent_key';
-- expected: CREATE UNIQUE INDEX uq_send_intent_key ... (organization_id, idempotency_key)
-- the index that makes "one open send per case" true in the database
SELECT indexdef FROM pg_indexes
 WHERE tablename = 'occurrence_send_intents' AND indexname = 'uq_send_intent_open';
-- expected: CREATE UNIQUE INDEX uq_send_intent_open ... (organization_id, occurrence_id, kind) WHERE state IN ('processing','uncertain')
-- the permission exists and the intended roles hold it
SELECT r.name, r.organization_id FROM custom_roles r
  JOIN role_permissions rp ON rp.custom_role_id = r.id
  JOIN permissions p ON p.id = rp.permission_id
 WHERE p.resource = 'occurrences' AND p.action = 'resolve_send' AND r.deleted_at IS NULL;
-- the other indexes: idx_send_intent_occurrence, idx_send_intent_pending (partial, state = 'processing'),
-- idx_send_intent_uncertain_recheck (partial, state = 'uncertain' AND reconciled_at IS NULL)
SELECT indexname FROM pg_indexes WHERE tablename = 'occurrence_send_intents' ORDER BY 1;
```

If `uq_send_intent_key` or `uq_send_intent_open` is missing, do **not** release the flow: without the first two requests with the same key can both send, and without the second two people can send to the same customer from one case at the same time.

**Rollback.** Going back to the previous binary (and frontend) does not require removing the table: the old code ignores it. **Do not drop the table or delete rows as part of a rollback**: the rows are the record of which keys were already used (and, for `uncertain`, of sends that may have reached the customer); removing them frees the keys and a repeated request would send again. Drop it only by a separate, explicit decision once no new-version client can still repeat a key. Rolling back backend and frontend together is the same coordination as deploying them. A previous binary that does not know `uq_send_intent_open` is stopped by it, not harmed: a send that finds the case taken fails with "nothing was sent" instead of overlapping. Drop that index only as part of the same separate, explicit decision.

## What the browser treats as an answer (frontend rules)

- Only the application's own answer **closes** an intention (the browser forgets the key): a body that carries the `send_id` (sent, failed or uncertain are answers about that send), or an application error envelope (`status: "error"`) with a 4xx status (key missing or invalid, not allowed, not found, key reused, closed window).
- A 4xx envelope closes the intention only on the **first** request under a key. On a **retry** it proves only that the retry was refused (409 after the contact data changed, 429, 401/403 after the session or access changed), not what became of the earlier request; the key stays and the page asks the server. Only a body with the `send_id`, or the server answering `sent`/`failed` to the state query, closes it.
- Every request under a key restarts the 60 s in-flight grace (`savedAt` is renewed), because the newest request may still be on its way.
- `409 send_pending` on a new intention means **another send of the case is open**: the key of the refused request was never used, so nothing stays pending for it. The page does not show an error: it asks `?open=true`, shows the pending state (and says another message is open when `own` is false) and offers to settle it only to someone holding `occurrences:resolve_send`.
- A 404 means "no claim for that send or key" **only** when it is the application's own `send_not_found`. Any other 404 (case or contact gone, a route a proxy or older backend does not know) concludes nothing: the record stays.
- The copy of the record kept in the page's memory obeys the same 24 h as the browser storage.
- Anything else is **no answer**: a timeout, a lost connection, and also a 5xx (or an envelope-less 4xx such as 413/429) from a proxy or load balancer, for example 502/503/504 while the backend restarts. The key is kept and the page asks the server (by `send_id` or by key); it does not send again.
- While an intention is unresolved, the form does not accept a **different text** for the same case: it does not mint a second key (that would be a second message while the first may be in flight or delivered). The text is locked and the page shows the pending state until the server says `sent` or `failed`.

## Compatibility note

`chat:write` is now also required on the other customer-sending endpoints (`SendMessage`, `SendMediaMessage`, `SendReaction`, `SendTemplateMessage`). Integrations or roles that sent without it now get 403. Review roles and API keys before the deploy.
