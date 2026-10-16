# Read-only 易协作 PM cycles

Run one configured cycle with:

```text
multica yixiezuo pm-sync --config-file pm-sync.json
```

The runtime supplies your workspace and task identity. The UTF-8 configuration contains source_host, source_project, assignee_emails, issue_type, project_id, and the exact triage_mention supplied by the user. Optional assignee_map values are member user_id values from `multica workspace member list --output json`; category_property_id names the configured category property. Never invent missing IDs or rewrite a supplied mention by name.

This command queries source BUGs, preserves source-key idempotency, applies changed source fields, separately reads tracked sources that disappear from discovery, and reports every failure. It reads all reconciliation pages before writes. A missing source or failed read never closes a task. Unchanged source fields do not undo local routing or classification. The single-issue manual import flow remains available to members separately.

The triage scan requires an empty category and owner plus backlog, actual Triage, or a “待分诊” description. Human-handled tasks are skipped. Each task has its own comment; the target's actual concurrency limit must be 1. The command reads the trigger receipt and retains the comment identity, so successful inputs are not re-posted on the next cycle. A failed trigger retries the same comment. Posted, queued, and classification completed are distinct outcomes.

Deliver the complete stdout report as this cycle's result, including on nonzero exit. Default output is Chinese Markdown; `--output json` provides structured receipts. The sections are 扫描时间, 查询范围, 本轮单数, 新增同步, 更新, 未分类转交, 异常与待人工确认. No change is explicitly reported. Do not hide a failed write or `invocation_not_allowed` / `target_unavailable` trigger.

Use only this command for the configured automation. Do not call source write tools, `publish`, `confirm`, or `bridge`; do not create extra synchronization tasks or send duplicate mentions yourself. The triager does not access 易协作, and PM does not make the triager's classification, claim, or reassignment decisions. No credentials belong in configuration or reports.
