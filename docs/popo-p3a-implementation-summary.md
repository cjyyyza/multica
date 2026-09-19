# P3a implementation summary

Group @, quoted/forwarded context, and `/new` `/clear` on POPO inbound. Shared Channel Engine already owned the control commands and selected-quote contract; this change only wires the bridge mapper so those paths run.

## Product behavior

- `chat.type=p2p` with `addressed_to_bot=true` is unchanged.
- `chat.type=group` is accepted only when `addressed_to_bot=true` (`@` the bot). Unaddressed group chatter returns `200 {accepted:false}` and is not persisted.
- Group sessions use `ChatTypeGroup` and the group chat id as the binding key, so they stay isolated from the sender's p2p session.
- Quote objects may include `text`, `sender_id`, and `sender_name` in addition to `message_id` (still accepting `msgId` / `uuid` / `messageId`).
- When quote text is present, `HasSelectedContext` is set and `Text` is `channel.FormatQuotedMessage(sender_name, quote.text)` plus the user's own body. `CommandText` stays the user's command, so a quoted `/issue` does not create an issue.
- Quote with a message id but no readable text still sets `HasSelectedContext` with `> [quoted content unavailable]`, so `/new` / `/clear` keep the selected quote.
- `/new` and `/clear` are not re-parsed in the POPO adapter. `CommandText` carries the directive; Router starts a new Chat or advances the context generation.
- Media is not ingested. Media-only drops; text plus unused media bytes still ingests the text. No `/api/popo/bridge/media`.

## Files

- `server/internal/integrations/popo/inbound_bridge.go` — group addressing, quote enrichment, ignore media bytes
- `server/internal/integrations/popo/inbound_test.go` — mapper coverage
- `server/internal/handler/popo_bridge_test.go` — unaddressed group drop; @-addressed group accepted and engine Handle runs
- `server/internal/handler/popo_p2_test.go` — shared inbound helper accepts chat type
- `server/internal/handler/popo_p3_test.go` — group session, quoted `/issue`, `/new`, `/clear`

DJ01Bot trees were not modified.

## Tests

Go unit tests (`integrations/popo`):

- addressed group maps to `ChatTypeGroup`; unaddressed group is dropped
- quote text is in `Text`, `CommandText` is user-only; quoted `/issue` stays out of `CommandText`
- quote id without text uses the unavailable placeholder
- `/new` / `/clear` keep the directive on `CommandText` and strip it from `Text`
- text+media ingests text; media-only drops

Go DB tests (`handler`):

- unaddressed group `accepted:false` with no persist; @-addressed group `accepted:true` and engine Handle writes dedup
- group @ creates a `chat_type=group` binding distinct from p2p
- quoted `/issue` does not create an issue; stored chat text includes the quote and the user body
- `/new` on POPO p2p starts a new Chat and retires the old route
- `/clear` on POPO p2p keeps the Chat and sets `force_fresh_session`

Engine tests relied on: `TestRouter_SelectedQuoteSurvivesControl`, `TestBareNewBehindAQuoteDoesNotPersistTheDirective`, `TestBareClearBehindAQuoteKeepsTheQuote`. Existing P2 quote-to-comment still uses `CommandText` for the member comment body.

## Verification

From `server/`:

```
go test ./internal/integrations/popo/ -count=1
go test ./internal/integrations/channel/engine/ -count=1 -run "Quoted|SelectedQuote|BareNewBehind|BareClearBehind|ParseControlCommand"
go test ./internal/handler/ -count=1 -run "TestPopo"
```

No real POPO. QR, media upload, cards, and streaming were not run.

## Not in this PR

QR scan, `/api/popo/bridge/media`, cards, streaming, Feishu/DingTalk/Telegram adapter changes, DJ01Bot `normalize_bridge_quote` still sending only `{message_id}`.
