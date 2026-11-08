# P4 native execution and changelist contract

This document describes the current implementation. P4 command syntax belongs to the installed `p4` binary; Multica does not maintain a parallel command or flag contract.

## Workspace binding

A P4 task uses an existing `in_place` local directory and a `perforce_depot` resource. The daemon resolves the client through the directory's `P4CONFIG`, verifies Owner/Host/Root, and matches the client to a configured depot or stream. It does not create a task client or download a separate depot copy.

Local directory bindings remain private to their `created_by` member. Workspace events expose only resource IDs, and claims expose bindings only to the executing runtime owner. Shared depot configuration remains workspace-visible.

The resolved `P4PORT`, `P4USER`, `P4CHARSET`, and `P4CLIENT` are injected into the agent environment. Native `p4` is available and is not shielded by fake `P4CONFIG` or `P4CLIENT` values. Multica-managed instructions and skills still use the task-private runtime directory rather than the depot.

## Native CLI and legacy daemon endpoints

Agents invoke the installed `p4` binary directly. Multica supplies the verified workspace environment but has no native-command proxy, daemon HTTP execution endpoint, argument serialization, output capture, or synthetic exit-code path. argv, stdin, stdout, stderr, timeout behavior, and exit status belong to the native process and its caller.

Workspace depot management uses the separate namespace `multica p4 registry list|add|remove`. Native file addition is always `p4 add ...`; `multica p4 add` is not a supported alias.

Legacy `/p4/run`, `/p4/status`, and `/p4/sync` endpoints return HTTP 410 with an upgrade message instead of reinterpreting old requests. There is no `perforce-native-v1` capability because native execution no longer crosses the daemon API.

## Task lifecycle

Binding supplies context only. Multica does not create or reuse a task changelist, snapshot human opens, register a pending CL, revert unchanged files, shelve, submit, or create an automatic Swarm review at task exit. The binding is forgotten when the run ends. Existing local-directory setup errors and preparation-lease behavior remain unchanged.

A resolved 易协作 ticket is informational runtime context. It does not gate file operations. When present, the brief recommends `refs #<ticket>` for a new CL description.

## Explicit changelist association

`multica p4 changelist link --changelist <number>` is a Multica integration, not a native P4 wrapper. It requires an active issue task and calls `POST /p4/changelist/link` with the task credential and an owned workdir. The daemon describes the CL, verifies that it belongs to the bound client, reads pending/shelved/submitted state and file counts, then calls the existing task changelist report route. The existing issue link, polling, renumbering and auto-advance behavior is reused.

Association is explicit: Multica does not guess task ownership from the newest CL or from every CL that changed during a run. The runtime brief teaches agents to link each CL they create or use. Agent close-out remains native and scoped: inspect `p4 opened`, clean only unchanged files opened for that task, and use `p4 shelve` when a pending review is needed; never revert the whole shared client.

## Swarm

`multica p4 swarm ...` remains a separate Multica command family. It uses the exact configured depot's Swarm origin and the host Helix ticket; a task cannot choose another credential destination. Requests do not follow redirects. When a bound request omits port, depot, and stream together, only those connection fields default to the binding's matched depot; user and charset can default from the same binding. Partial or explicit refs still go through the configured-depot checks.

`swarm create` requires an explicit changelist, calls Swarm directly, and never shelves first or inherits a task changelist. Pending reviews therefore require an explicit native `p4 shelve`; submitted CLs create post-commit reviews. Create/link attach the review to the current issue where applicable. In an issue task they then call the same explicit changelist-link path, so the CL and review are both associated without a second command. If CL association fails after the review was linked, the CLI preserves that fact and tells the user to retry `multica p4 changelist link` rather than creating another review.

## Changelist records and polling

The existing issue CL model remains: `pending`, `shelved`, `submitted`, and `deleted`, with current/original numbers, Swarm state, manual link/unlink, realtime invalidation, and issue auto-advance when every linked CL is submitted and no PR owns completion.

Daemon polling uses only currently configured depots and verified endpoint relationships. Existing leases, generations, submission timestamps, and `rsh:` exclusion remain in force. Native task execution does not create new task-attributed CL rows automatically. Rows are created by the explicit Multica changelist-link integration, Swarm create/link, or the existing manual issue UI.

Receipt types persisted by an older daemon remain decodable so the durable terminal-report queue can drain. Explicit links use the current server report route directly and return failures to the caller for retry.

## Removed configuration

`allow_submit`, `allow_sync`, `allow_unshelve`, `allow_exclusive`, and `no_auto_review` no longer control execution and are removed from depot types, settings, and CLI shortcuts. Old JSON fields are ignored and omitted when a depot is normalized again.

## Verification

Use test-created fake P4 executables for default tests. Focused coverage includes real client environment injection, no automatic close-out, the Multica-only `registry` / `changelist` / `swarm` CLI namespace, verified CL association with shelf state, Swarm bound-depot defaults and automatic CL association without implicit shelve or changelist, depot normalization, and existing CL polling. Real shared P4 servers and user-installed agent CLIs are not default test fixtures.
