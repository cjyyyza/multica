# P3 media implementation summary

Windows fetches POPO files and uploads them to Multica. The API never downloads from POPO. Staging is not a chat/issue attachment until the sender is a workspace member and the inbound message exists.

## Windows upload order

Prefer this order so `ResolveMedia` does not wait:

1. Persist the inbound event locally.
2. `POST /api/popo/bridge/media/sessions` (keyed by `event_id` + `index` + bridge). Optional `robot_id` binds the session to the installation; otherwise the inbound robot resolves it later.
3. `PUT /api/popo/bridge/media/sessions/{id}` with the raw bytes (or multipart `file`). Max 20 MiB.
4. `POST /api/popo/bridge/inbound` with the same `media` descriptors.

Sessions created before inbound are allowed if they belong to this bridge. Promotion still waits until the inbound message exists and the sender is a member.

If inbound arrives while a session is still `pending`, `ResolveMedia` polls until the engine media deadline (same 45s budget as Slack/WeCom). It does not busy-loop. Missing sessions fail visibly rather than pretending the message is fully attached.

DJ01Bot was not modified.

## Inbound descriptors

`POST /api/popo/bridge/inbound` `media` array:

```json
[{
  "index": 0,
  "kind": "image"|"file"|"audio"|"video",
  "filename": "a.png",
  "mime_type": "image/png",
  "size_bytes": 1234,
  "popo_file_id": "optional-platform-id"
}]
```

`HasMedia` is true when the array is non-empty (pure decode, no I/O). Text may be empty for media-only. Group `@` + media is accepted when `addressed_to_bot` is true.

Pending placeholders in `Text` are `[Image]` / `[File]` / `[Audio]` / `[Video]`. A missing or failed index is rewritten to `[file: name — failed]` (or `[image: name — failed]`) and is not bound.

## Staging and promotion

- Auth: bridge bearer. Store hashes/ids only, never the token. Session TTL is 10 minutes.
- `PUT` writes object storage **after** a `channel_media_pending_object` intent row (Slack `media_ingest.go` / `MediaIntentLedger`). Session becomes `uploaded`. No chat/issue attachment is created in `PUT`.
- `NewPopoResolverSet` includes `engine.MediaResolver`. After member bind, `ResolveMedia` promotes uploaded sessions through `BindMedia`. Unbound senders are not promoted.
- Partial success is visible: one of two files can bind while the other stays a failure placeholder.

## Outbound

Chat/issue follow output with attachments, or text longer than 4000 runes, enqueues a `send` payload with `text` (may include a Multica link) and:

```json
"attachments": [{
  "attachment_id": "...",
  "filename": "a.png",
  "mime_type": "image/png",
  "download_path": "/api/popo/bridge/media/outbound/{attachmentId}"
}]
```

`download_path` is a bridge-token GET. The grant is scoped to the send command's workspace/installation. Windows local paths are never in the payload.

Partial outbound failure: `delivered` may still be used for successful text, with `error` listing which files failed. Do not claim every attachment was sent.

## Schema (527–533)

No FKs or cascades. Concurrent indexes are their own migrations.

- `popo_media_staging`
- `popo_outbound_media_grant`

Workspace teardown deletes both. `make sqlc` was run.

## Tests

Go unit tests (`integrations/popo`):

- `HasMedia` true/false
- uploaded staging promotes a `MediaRef`
- unbound sender does not promote
- partial staging (1 of 2) leaves `[file: name — failed]`
- long-text clip + send payload attachments
- group `@` + image descriptor accepted
- media-only inbound accepted

Go DB tests (`handler`):

- staging PUT then inbound binds a chat attachment
- unbound sender does not bind
- partial staging leaves failure copy on the stored message
- group `@` + image creates a group session
- outbound attachment grant GET with the bridge token
- long text enqueue carries a file grant

## Verification

From `server/`:

```
go test ./internal/integrations/popo/ -count=1
go test ./internal/handler/ -count=1 -run "TestPopo"
```

No real POPO. QR scan was not implemented.

## Not in this PR

QR scan, DJ01Bot upload client, cards, streaming.
