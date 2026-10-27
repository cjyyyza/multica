import "./env";

import { createHash, randomBytes, randomUUID } from "crypto";
import { expect, test, type Page } from "@playwright/test";
import pg from "pg";
import { TestApiClient } from "./fixtures";

const API_BASE = process.env.NEXT_PUBLIC_API_URL || `http://localhost:${process.env.PORT || "8080"}`;
const DATABASE_URL = process.env.DATABASE_URL ?? "postgres://multica:multica@localhost:5432/multica?sslmode=disable";

type ApiResult = { status: number; body: any };

async function apiRequest({
  path,
  token,
  workspaceId,
  method = "GET",
  body,
  idempotencyKey,
}: {
  path: string;
  token: string;
  workspaceId?: string;
  method?: string;
  body?: unknown;
  idempotencyKey?: string;
}): Promise<ApiResult> {
  const response = await fetch(`${API_BASE}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      ...(workspaceId ? { "X-Workspace-ID": workspaceId } : {}),
      ...(idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {}),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  const text = await response.text();
  let payload: any = null;
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = text;
    }
  }
  return { status: response.status, body: payload };
}

function expectApi(result: ApiResult, status: number) {
  expect(result.status, JSON.stringify(result.body)).toBe(status);
  return result.body;
}

function sha256(value: string) {
  return createHash("sha256").update(value).digest("hex");
}

function canonicalJSON(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(",")}]`;
  if (value !== null && typeof value === "object") {
    const object = value as Record<string, unknown>;
    return `{${Object.keys(object).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSON(object[key])}`).join(",")}}`;
  }
  return JSON.stringify(value);
}

function sourceFixture(externalId: string) {
  const source = {
    host: "dj01.pm.netease.com",
    id: externalId,
    url: `https://dj01.pm.netease.com/issues/${externalId}`,
  };
  const snapshot = {
    source,
    title: `E2E Requirement ${externalId}`,
    description: "Source material for the real requirement flow",
    status: "open",
    priority: "normal",
    updated_at: "",
    lock_version: 3,
    attachments: [],
    comments: [],
    statuses: [],
    warnings: [],
    digest: "",
    fields: [],
  };
  const digest = sha256(JSON.stringify({ ...snapshot, attachments: null, statuses: null, warnings: null }));
  return { source, snapshot: { ...snapshot, digest }, digest };
}

async function mintTaskToken(
  db: pg.Client,
  { taskId, agentId, workspaceId, userId }: { taskId: string; agentId: string; workspaceId: string; userId: string },
) {
  const token = `mat_${randomBytes(32).toString("hex")}`;
  await db.query(
    `INSERT INTO task_token (token_hash, task_id, agent_id, workspace_id, user_id, expires_at)
     VALUES ($1, $2, $3, $4, $5, now() + interval '1 hour')`,
    [sha256(token), taskId, agentId, workspaceId, userId],
  );
  return token;
}

async function markTaskRunning(db: pg.Client, taskId: string) {
  const result = await db.query(
    `UPDATE agent_task_queue
     SET status = 'running', dispatched_at = COALESCE(dispatched_at, now()),
         started_at = COALESCE(started_at, now()), completed_at = NULL
     WHERE id = $1 AND status IN ('queued', 'deferred', 'dispatched')`,
    [taskId],
  );
  expect(result.rowCount).toBe(1);
}

async function completeTask(api: TestApiClient, workspaceId: string, taskId: string) {
  const result = await apiRequest({
    path: `/api/daemon/tasks/${taskId}/complete`,
    token: api.getToken()!,
    workspaceId,
    method: "POST",
    body: { output: `E2E runtime completed ${taskId}` },
  });
  const completed = expectApi(result, 200);
  expect(completed.id).toBe(taskId);
  expect(completed.status).toBe("completed");
}

async function getRequirement(api: TestApiClient, workspaceId: string, requirementId: string) {
  return expectApi(await apiRequest({
    path: `/api/workspaces/${workspaceId}/requirements/${requirementId}`,
    token: api.getToken()!,
    workspaceId,
  }), 200);
}

async function expectEntityCounts(
  db: pg.Client,
  requirementId: string,
  expected: { chats: number; issues: number; approvals: number; planTasks: number; executeTasks: number },
) {
  const result = await db.query<{
    chats: number;
    issues: number;
    origin_issues: number;
    approvals: number;
    plan_tasks: number;
    execute_tasks: number;
  }>(
    `SELECT
       (SELECT count(*) FROM chat_session cs
        JOIN requirement r ON r.chat_session_id = cs.id WHERE r.id = $1)::int AS chats,
       (SELECT count(*) FROM issue i
        JOIN requirement r ON r.issue_id = i.id WHERE r.id = $1)::int AS issues,
       (SELECT count(*) FROM issue i
        JOIN requirement r ON r.chat_session_id = i.origin_id
        WHERE r.id = $1 AND i.origin_type = 'requirement_chat')::int AS origin_issues,
       (SELECT count(*) FROM requirement_approval WHERE requirement_id = $1)::int AS approvals,
       (SELECT count(*) FROM agent_task_queue WHERE requirement_id = $1 AND mode = 'plan')::int AS plan_tasks,
       (SELECT count(*) FROM agent_task_queue WHERE requirement_id = $1 AND mode = 'execute')::int AS execute_tasks`,
    [requirementId],
  );
  expect(result.rows[0]).toEqual({
    chats: expected.chats,
    issues: expected.issues,
    origin_issues: expected.issues,
    approvals: expected.approvals,
    plan_tasks: expected.planTasks,
    execute_tasks: expected.executeTasks,
  });
}

async function screenshot(page: Page, testInfo: any, name: string) {
  await page.screenshot({ path: testInfo.outputPath(`${name}.png`), fullPage: true });
}

for (const viewport of [{ width: 1440, height: 1100 }, { width: 851, height: 900 }]) {
test(`first Requirement intake, deep link, execution and human review (${viewport.width}px)`, async ({ page }, testInfo) => {
  await page.setViewportSize(viewport);
  test.setTimeout(180_000);
  const api = new TestApiClient();
  const runId = `${Date.now().toString(36)}-${randomUUID().replaceAll("-", "").slice(0, 10)}`;
  const externalId = `${String(Date.now()).slice(-8)}${String(Math.floor(Math.random() * 1000)).padStart(3, "0")}`;
  const slug = `e2e-requirement-${runId}`;
  const email = `${slug}@multica.ai`;
  const key = (action: string) => `requirement-e2e-${runId}-${action}`;
  const db = new pg.Client(DATABASE_URL);

  try {
    const login = await api.login(email, "E2E Requirement User");
    const userId = login.user.id as string;
    const workspace = await api.ensureWorkspace("E2E Requirement Flow", slug);
    await api.markUserOnboarded();
    await db.connect();

    const runtime = await db.query<{ id: string }>(
      `INSERT INTO agent_runtime (
         workspace_id, name, runtime_mode, provider, status, device_info,
         metadata, owner_id, last_seen_at
       ) VALUES ($1, $2, 'cloud', 'claude', 'online', 'E2E fixture', '{}'::jsonb, $3, now())
       RETURNING id::text`,
      [workspace.id, `Requirement runtime ${runId}`, userId],
    );
    const agent = await db.query<{ id: string }>(
      `INSERT INTO agent (
         workspace_id, name, description, instructions, runtime_mode, runtime_config,
         runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id
       ) VALUES ($1, $2, '', '', 'cloud', '{}'::jsonb, $3, 'private', 'private', 1, $4)
       RETURNING id::text`,
      [workspace.id, `Requirement Claude ${runId}`, runtime.rows[0]!.id, userId],
    );
    const agentId = agent.rows[0]!.id;
    const { source, snapshot, digest } = sourceFixture(externalId);
    const installation = await db.query<{ id: string }>(
      `INSERT INTO channel_installation (
         workspace_id, agent_id, channel_type, installer_user_id, status, config
       ) VALUES ($1, $2, 'popo', $3, 'active', '{}'::jsonb) RETURNING id::text`,
      [workspace.id, agentId, userId],
    );
    const installationId = installation.rows[0]!.id;
    const sourceChat = await db.query<{ id: string }>(
      `INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status)
       VALUES ($1, $2, $3, $4, 'active') RETURNING id::text`,
      [workspace.id, agentId, userId, `Source channel fixture ${runId}`],
    );
    const channelChatId = `requirement-e2e-${runId}`;
    const binding = await db.query<{ id: string }>(
      `INSERT INTO channel_chat_session_binding (
         chat_session_id, installation_id, channel_type, channel_chat_id,
         chat_type, route_revision, config
       ) VALUES ($1, $2, 'popo', $3, 'group', 7, '{}'::jsonb) RETURNING id::text`,
      [sourceChat.rows[0]!.id, installationId, channelChatId],
    );
    const operation = await db.query<{ id: string }>(
      `INSERT INTO yixiezuo_operation (
         workspace_id, requested_by, kind, state, payload, result
       ) VALUES ($1, $2, 'preview', 'succeeded', $3::jsonb, $4::jsonb) RETURNING id::text`,
      [
        workspace.id,
        userId,
        JSON.stringify({
          source,
          channel: { installation_id: installationId, chat_id: channelChatId, chat_type: "group" },
        }),
        JSON.stringify(snapshot),
      ],
    );
    const operationId = operation.rows[0]!.id;
    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token);
      localStorage.setItem("multica:chat:isOpen", "false");
    }, api.getToken()!);
    // Seeded source evidence keeps browser acceptance independent of external PM availability.
    await page.route(`**/api/workspaces/${workspace.id}/yixiezuo/preview`, (route) =>
      route.fulfill({ json: { id: operationId, kind: "preview", state: "succeeded", snapshot, error: "" } }),
    );
    let releaseOpen!: () => void;
    const openGate = new Promise<void>((resolve) => { releaseOpen = resolve; });
    await page.route(`**/api/workspaces/${workspace.id}/requirements/*/open`, async (route) => {
      await openGate;
      await route.continue();
    });
    await page.goto(`/${workspace.slug}/chat`, { waitUntil: "domcontentloaded" });
    await page.getByRole("button", { name: "New requirement", exact: true }).click();
    const intake = page.getByRole("dialog", { name: "New requirement" });
    await intake.getByRole("button", { name: "Use an existing source link", exact: true }).click();
    await intake.getByLabel("Source URL").fill(source.url);
    await intake.getByRole("button", { name: "Preview source", exact: true }).click();
    await expect(intake.getByLabel("Agent", { exact: true })).toHaveValue(agentId);
    await screenshot(page, testInfo, "00-first-intake");
    const alternateViewport = viewport.width === 1440
      ? { width: 851, height: 900 }
      : { width: 1440, height: 1100 };
    await page.setViewportSize(alternateViewport);
    await expect(page.getByRole("button", { name: "Create requirement", exact: true })).toBeVisible();
    await expect(page.getByLabel("Source URL", { exact: true })).toHaveValue(source.url);
    const intakeBounds = await page.getByRole("dialog").boundingBox();
    expect(intakeBounds).not.toBeNull();
    expect(intakeBounds!.x).toBeGreaterThanOrEqual(0);
    expect(intakeBounds!.x + intakeBounds!.width).toBeLessThanOrEqual(alternateViewport.width);
    await screenshot(page, testInfo, "00-first-intake-resized");
    await page.setViewportSize(viewport);
    await expect(page.getByRole("button", { name: "Create requirement", exact: true })).toBeVisible();
    const openResponsePromise = page.waitForResponse((response) =>
      response.request().method() === "POST" && /\/requirements\/[^/]+\/open$/.test(response.url()),
      { timeout: 60_000 },
    );
    const createResponsePromise = page.waitForResponse((response) =>
      response.request().method() === "POST" && response.url().endsWith(`/workspaces/${workspace.id}/requirements`),
    );
    await intake.getByRole("button", { name: "Create requirement", exact: true }).click();
    const createResponse = await createResponsePromise;
    expect(createResponse.status(), await createResponse.text()).toBe(201);
    const created = await createResponse.json();
    const requirementId = created.id as string;
    expect(created).toMatchObject({
      workspaceId: workspace.id,
      revision: 1,
      state: "intake_pending",
      intakeState: "intake_pending",
      externalId,
      chatSessionId: null,
      issueId: null,
      latestCandidate: null,
      intakeOperationId: operationId,
    });
    await expectEntityCounts(db, requirementId, { chats: 0, issues: 0, approvals: 0, planTasks: 0, executeTasks: 0 });

    releaseOpen();
    const openResponse = await openResponsePromise;
    expect(openResponse.status()).toBe(200);
    const opened = await openResponse.json();
    const chatId = opened.session.id as string;
    const planTaskId = opened.pendingTask.task_id as string;
    expect(opened.pendingTask.status).toBe("queued");
    expect(opened.requirement).toMatchObject({
      id: requirementId,
      revision: 2,
      state: "clarifying",
      chatSessionId: chatId,
      issueId: null,
    });
    await expect(page).toHaveURL((url) =>
      url.searchParams.get("session") === chatId && url.searchParams.get("requirement") === requirementId,
    { timeout: 30_000 });
    await expect(page.getByTestId("requirement-flow-panel")).toBeVisible();
    const planTask = await db.query(
      `SELECT mode, status, requirement_id::text, chat_session_id::text, issue_id::text
       FROM agent_task_queue WHERE id = $1`,
      [planTaskId],
    );
    expect(planTask.rows[0]).toMatchObject({
      mode: "plan",
      status: "queued",
      requirement_id: requirementId,
      chat_session_id: chatId,
      issue_id: null,
    });
    await expectEntityCounts(db, requirementId, { chats: 1, issues: 0, approvals: 0, planTasks: 1, executeTasks: 0 });
    await screenshot(page, testInfo, "01-deep-link-open");

    await markTaskRunning(db, planTaskId);
    const planToken = await mintTaskToken(db, { taskId: planTaskId, agentId, workspaceId: workspace.id, userId });
    const candidate = {
      goal: `Ship requirement ${externalId}`,
      nonGoals: ["No real POPO or PMMCP side effects"],
      scope: ["Requirement planning and review"],
      constraints: ["Use the existing Chat and exactly one approved Issue"],
      acceptanceCriteria: ["Reject once, execute again, then accept"],
      projectId: null,
      agentId,
    };
    const candidateDigest = sha256(canonicalJSON(candidate));
    const submitted = expectApi(await apiRequest({
      path: `/api/workspaces/${workspace.id}/requirements/${requirementId}/candidates`,
      token: planToken,
      workspaceId: workspace.id,
      method: "POST",
      idempotencyKey: key("candidate"),
      body: { expectedRevision: 2, snapshot: candidate },
    }), 200);
    expect(submitted).toMatchObject({ revision: 3, state: "awaiting_approval", issueId: null });
    expect(submitted.latestCandidate).toMatchObject({
      requirementId,
      revision: 3,
      digest: candidateDigest,
      createdByTaskId: planTaskId,
      snapshot: candidate,
    });
    const candidateRows = await db.query(
      `SELECT digest, created_by_task_id::text FROM requirement_candidate WHERE requirement_id = $1`,
      [requirementId],
    );
    expect(candidateRows.rows).toEqual([{ digest: candidateDigest, created_by_task_id: planTaskId }]);
    await expectEntityCounts(db, requirementId, { chats: 1, issues: 0, approvals: 0, planTasks: 1, executeTasks: 0 });
    await completeTask(api, workspace.id, planTaskId);
    await page.reload({ waitUntil: "domcontentloaded" });
    await expect(page.getByRole("button", { name: "Review and approve", exact: true })).toBeVisible({ timeout: 30_000 });
    await screenshot(page, testInfo, "02-candidate-awaiting-approval");

    const approveResponsePromise = page.waitForResponse((response) =>
      response.request().method() === "POST" && response.url().endsWith(`/requirements/${requirementId}/approve`),
    );
    await page.getByRole("button", { name: "Review and approve", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Approve and create Issue", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Create Issue and start agent", exact: true }).click();
    const approveResponse = await approveResponsePromise;
    expect(approveResponse.status()).toBe(200);
    expect(approveResponse.request().postDataJSON()).toEqual({
      expectedRevision: 3,
      candidateId: submitted.latestCandidate.id,
      candidateDigest,
    });
    const approved = await approveResponse.json();
    const issueId = approved.issue.id as string;
    expect(approved.requirement).toMatchObject({
      id: requirementId,
      state: "executing",
      chatSessionId: chatId,
      issueId,
    });
    expect(approved.issue).toMatchObject({
      status: "todo",
      origin_type: "requirement_chat",
      origin_id: chatId,
      assignee_type: "agent",
      assignee_id: agentId,
    });
    const approval = await db.query<{
      initial_task_id: string;
      issue_id: string;
      candidate_digest: string;
    }>(
      `SELECT initial_task_id::text, issue_id::text, candidate_digest
       FROM requirement_approval WHERE requirement_id = $1`,
      [requirementId],
    );
    expect(approval.rows).toEqual([{ initial_task_id: expect.any(String), issue_id: issueId, candidate_digest: candidateDigest }]);
    const initialTaskId = approval.rows[0]!.initial_task_id;
    const initialTask = await db.query(
      `SELECT mode, status, requirement_id::text, issue_id::text FROM agent_task_queue WHERE id = $1`,
      [initialTaskId],
    );
    expect(initialTask.rows[0]).toMatchObject({ mode: "execute", status: "queued", requirement_id: requirementId, issue_id: issueId });
    await expectEntityCounts(db, requirementId, { chats: 1, issues: 1, approvals: 1, planTasks: 1, executeTasks: 1 });
    await expect(page.getByRole("status", { name: "Requirement flow", exact: true })).toContainText("Executing");
    await expect(page.getByRole("link", { name: "Open Issue", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Send", exact: true })).toBeDisabled();
    const chatMessagesBefore = await db.query<{ count: number }>(
      `SELECT count(*)::int AS count FROM chat_message WHERE chat_session_id = $1`,
      [chatId],
    );
    const readOnlyAttempt = await apiRequest({
      path: `/api/chat/sessions/${chatId}/messages`,
      token: api.getToken()!,
      workspaceId: workspace.id,
      method: "POST",
      body: { content: "This must not create another requirement run" },
    });
    expectApi(readOnlyAttempt, 409);
    expect(readOnlyAttempt.body.code).toBe("requirement_issue_already_exists");
    const chatMessagesAfter = await db.query<{ count: number }>(
      `SELECT count(*)::int AS count FROM chat_message WHERE chat_session_id = $1`,
      [chatId],
    );
    expect(chatMessagesAfter.rows[0]!.count).toBe(chatMessagesBefore.rows[0]!.count);
    await screenshot(page, testInfo, "03-approved-read-only");

    await markTaskRunning(db, initialTaskId);
    const initialExecuteToken = await mintTaskToken(db, { taskId: initialTaskId, agentId, workspaceId: workspace.id, userId });
    const beforeFirstReview = await getRequirement(api, workspace.id, requirementId);
    const directReview = expectApi(await apiRequest({
      path: `/api/issues/${issueId}`,
      token: initialExecuteToken,
      workspaceId: workspace.id,
      method: "PUT",
      body: { status: "in_review", expected_revision: beforeFirstReview.issue.revision },
    }), 409);
    expect(directReview.code).toBe("requirement_status_transition_forbidden");
    await completeTask(api, workspace.id, initialTaskId);
    const inFirstReview = await getRequirement(api, workspace.id, requirementId);
    expect(inFirstReview).toMatchObject({ state: "in_review", issueId, chatSessionId: chatId });
    expect(inFirstReview.run).toMatchObject({ id: initialTaskId, mode: "execute", status: "completed" });
    expect(inFirstReview.issue.revision).toBeGreaterThan(beforeFirstReview.issue.revision);
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.getByTestId("requirement-flow-panel").getByRole("button", { name: "Details", exact: true }).click();
    await expect(page.getByText("Run completed; the requirement is not done yet.", { exact: true })).toBeVisible({ timeout: 30_000 });
    await expect(page.getByRole("button", { name: "Accept", exact: true })).toBeVisible();
    const completedFirstReview = await getRequirement(api, workspace.id, requirementId);
    expect(completedFirstReview).toMatchObject({ state: "in_review" });
    expect(completedFirstReview.run).toMatchObject({ id: initialTaskId, status: "completed" });
    expect(completedFirstReview.issue.status).toBe("in_review");
    await screenshot(page, testInfo, "04-first-review-run-completed-not-done");

    const rejectResponsePromise = page.waitForResponse((response) =>
      response.request().method() === "POST" && response.url().endsWith(`/requirements/${requirementId}/reject`),
    );
    await page.getByRole("button", { name: "Request changes", exact: true }).click();
    const reason = `E2E rejection ${externalId}: run one more execution pass`;
    await page.getByLabel("Reason", { exact: true }).fill(reason);
    await page.getByRole("button", { name: "Request changes", exact: true }).click();
    const rejectResponse = await rejectResponsePromise;
    expect(rejectResponse.status()).toBe(200);
    const rejected = await rejectResponse.json();
    expect(rejected).toMatchObject({ state: "executing", issueId, chatSessionId: chatId });
    expect(rejected.issue.status).toBe("in_progress");
    expect(rejected.run.id).not.toBe(initialTaskId);
    const retryTaskId = rejected.run.id as string;
    const rejection = await db.query(
      `SELECT content FROM comment WHERE issue_id = $1 AND author_type = 'member' AND content = $2`,
      [issueId, reason],
    );
    expect(rejection.rows).toEqual([{ content: reason }]);
    await expectEntityCounts(db, requirementId, { chats: 1, issues: 1, approvals: 1, planTasks: 1, executeTasks: 2 });
    const persistedApproval = await db.query(
      `SELECT initial_task_id::text FROM requirement_approval WHERE requirement_id = $1`,
      [requirementId],
    );
    expect(persistedApproval.rows).toEqual([{ initial_task_id: initialTaskId }]);
    await screenshot(page, testInfo, "05-rejected-new-execution");

    await markTaskRunning(db, retryTaskId);
    await completeTask(api, workspace.id, retryTaskId);
    const inSecondReview = await getRequirement(api, workspace.id, requirementId);
    expect(inSecondReview).toMatchObject({ state: "in_review", issueId });
    expect(inSecondReview.run).toMatchObject({ id: retryTaskId, mode: "execute", status: "completed" });
    await page.reload({ waitUntil: "domcontentloaded" });
    await page.getByTestId("requirement-flow-panel").getByRole("button", { name: "Details", exact: true }).click();
    await expect(page.getByText("Run completed; the requirement is not done yet.", { exact: true })).toBeVisible({ timeout: 30_000 });
    await expect(page.getByRole("button", { name: "Accept", exact: true })).toBeVisible();

    const acceptResponsePromise = page.waitForResponse((response) =>
      response.request().method() === "POST" && response.url().endsWith(`/requirements/${requirementId}/accept`),
    );
    await page.getByRole("button", { name: "Accept", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Accept and publish to Yixiezuo", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Accept and publish", exact: true }).click();
    const acceptResponse = await acceptResponsePromise;
    expect(acceptResponse.status()).toBe(200);
    const accepted = await acceptResponse.json();
    expect(accepted).toMatchObject({ state: "done", issueId, chatSessionId: chatId });
    expect(accepted.issue.status).toBe("done");
    expect(accepted.run).toMatchObject({ id: retryTaskId, status: "completed" });
    await expectEntityCounts(db, requirementId, { chats: 1, issues: 1, approvals: 1, planTasks: 1, executeTasks: 2 });
    const publish = await db.query(
      `SELECT state, error FROM yixiezuo_operation WHERE issue_id = $1 AND kind = 'publish'`,
      [issueId],
    );
    expect(publish.rows).toHaveLength(1);
    expect(publish.rows[0].state).toBe("pending");
    expect(publish.rows[0].error ?? "").toMatch(/^$|pmmcp_write is disabled/);
    await expect(page.getByText("Run completed; the requirement is not done yet.", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Accept", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Request changes", exact: true })).toHaveCount(0);
    await screenshot(page, testInfo, "06-accepted-done");
  } finally {
    await api.deleteWorkspace();
    await db.end().catch(() => undefined);
  }
});
}
