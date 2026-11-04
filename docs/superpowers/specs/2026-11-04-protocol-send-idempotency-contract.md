# Protocol send: idempotency and state contract

Endpoints (all need `occurrences:write`, `chat:write` and a valid tie to the case/channel; another organization or case is a 404):

- `POST /api/occurrences/{id}/send-protocol`: header `Idempotency-Key: <uuid>` is **required** (400 `idempotency_key_required` / `idempotency_key_invalid`). Body `{ "message"?: string }`.
- `GET /api/occurrences/{id}/protocol-sends/{send_id}`: state of one send.
- `GET /api/occurrences/{id}/protocol-sends?idempotency_key=<uuid>`: the same, for a caller that never saw the answer and does not know the `send_id`. Read-only; never claims the key, never sends. 404 = the server holds no claim for the key (the request never arrived or is still in flight): it is **not** proof that nothing was sent until a request in flight has ended (the server gives up on the provider after 30 s).

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

- `sent` and `failed` records are kept 90 days after they finish; `uncertain` records 90 days after they were reconciled. `processing` and `uncertain` not yet reconciled are **never** removed.
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

## Deployment: migration, order and rollback

**Required migration.** The table `occurrence_send_intents` is created by GORM AutoMigrate (`internal/database/postgres.go`), and AutoMigrate only runs when the backend starts with the `-migrate` flag (`whatomate server -migrate`). **Without it the table does not exist and the protocol send does not work** (every `send-protocol` fails; the recovery endpoints fail too). The migration is additive: it only creates a new table and its indexes; no existing table or row is touched.

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
-- the other indexes: idx_send_intent_occurrence, idx_send_intent_pending (partial, state = 'processing')
SELECT indexname FROM pg_indexes WHERE tablename = 'occurrence_send_intents' ORDER BY 1;
```

If `uq_send_intent_key` is missing, do **not** release the flow: without the UNIQUE index two requests with the same key can both send.

**Rollback.** Going back to the previous binary (and frontend) does not require removing the table: the old code ignores it. **Do not drop the table or delete rows as part of a rollback**: the rows are the record of which keys were already used (and, for `uncertain`, of sends that may have reached the customer); removing them frees the keys and a repeated request would send again. Drop it only by a separate, explicit decision once no new-version client can still repeat a key. Rolling back backend and frontend together is the same coordination as deploying them.
