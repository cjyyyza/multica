import "./env";

import { randomUUID } from "crypto";
import { expect, test } from "@playwright/test";
import pg from "pg";
import { TestApiClient } from "./fixtures";

const API_BASE = process.env.NEXT_PUBLIC_API_URL || `http://localhost:${process.env.PORT || "8080"}`;
const DATABASE_URL = process.env.DATABASE_URL ?? "postgres://multica:multica@localhost:5432/multica?sslmode=disable";
const SOURCE_HOST = "dj01.pm.netease.com";
const DEFAULT_TEMPLATE_ID = "108402";

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

async function seedAgent(
  db: pg.Client,
  { workspaceId, userId, label }: { workspaceId: string; userId: string; label: string },
): Promise<string> {
  const runtime = await db.query<{ id: string }>(
    `INSERT INTO agent_runtime (
       workspace_id, name, runtime_mode, provider, status, device_info,
       metadata, owner_id, last_seen_at
     ) VALUES ($1, $2, 'cloud', 'claude', 'online', 'E2E fixture', '{}'::jsonb, $3, now())
     RETURNING id::text`,
    [workspaceId, `Idea intake runtime ${label}`, userId],
  );
  const agent = await db.query<{ id: string }>(
    `INSERT INTO agent (
       workspace_id, name, description, instructions, runtime_mode, runtime_config,
       runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id
     ) VALUES ($1, $2, '', '', 'cloud', '{}'::jsonb, $3, 'private', 'private', 1, $4)
     RETURNING id::text`,
    [workspaceId, `Idea intake agent ${label}`, runtime.rows[0]!.id, userId],
  );
  return agent.rows[0]!.id;
}

/** Act as the member's Windows bridge: claim the queued draft, then report its outcome. */
async function claimDraft(api: TestApiClient, workspaceId: string) {
  const claimed = expectApi(await apiRequest({
    path: `/api/workspaces/${workspaceId}/yixiezuo/bridge/claim`,
    token: api.getToken()!,
    workspaceId,
    method: "POST",
  }), 200);
  expect(claimed.operation, "the bridge must claim the queued idea draft").not.toBeNull();
  return claimed.operation;
}

async function completeDraft(
  api: TestApiClient,
  workspaceId: string,
  operationId: string,
  body: { state: string; lease_token: string; snapshot?: unknown; error?: string },
) {
  return expectApi(await apiRequest({
    path: `/api/workspaces/${workspaceId}/yixiezuo/operations/${operationId}/complete`,
    token: api.getToken()!,
    workspaceId,
    method: "POST",
    body,
  }), 200);
}

function draftSnapshot(operationId: string, title: string, externalId: string) {
  return {
    source: {
      host: SOURCE_HOST,
      id: externalId,
      url: `https://${SOURCE_HOST}/issues/${externalId}`,
    },
    title,
    // The bridge builds this description: the idea body, the 待澄清 marker and
    // the draft creation marker the server validates before handing off.
    description: `<p>待澄清</p><p>From the chat idea intake</p><p>[Multica draft ${operationId}]</p>`,
    status: "open",
    priority: "normal",
    updated_at: "2026-10-27T00:00:00Z",
    lock_version: 0,
    attachments: [],
    comments: [],
    statuses: [],
    warnings: [],
    digest: "",
    fields: [],
  };
}

test("idea-first intake hands a confirmed draft to a Requirement Chat", async ({ page }, testInfo) => {
  test.setTimeout(120_000);
  const api = new TestApiClient();
  const runId = `${Date.now().toString(36)}-${randomUUID().replaceAll("-", "").slice(0, 8)}`;
  const slug = `e2e-idea-intake-${runId}`;
  const email = `${slug}@multica.ai`;
  const externalId = `${String(Date.now()).slice(-9)}`;
  const idea = "Remember the email on the login page\nOnly the last used address, no prefill of the password";
  const title = idea.split("\n")[0]!;
  const db = new pg.Client(DATABASE_URL);

  try {
    const login = await api.login(email, "E2E Idea Intake User");
    const userId = login.user.id as string;
    const workspace = await api.ensureWorkspace("E2E Idea Intake", slug);
    await api.markUserOnboarded();
    await db.connect();
    const agentId = await seedAgent(db, { workspaceId: workspace.id, userId, label: runId });

    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token);
      localStorage.setItem("multica:chat:isOpen", "false");
    }, api.getToken()!);
    await page.goto(`/${workspace.slug}/chat`, { waitUntil: "domcontentloaded" });

    await page.getByRole("button", { name: "New requirement", exact: true }).click();
    const intake = page.getByRole("dialog", { name: "New requirement" });
    await expect(intake.getByLabel("Idea", { exact: true })).toBeVisible();
    await expect(intake.getByLabel("Source URL", { exact: true })).toHaveCount(0);
    await expect(intake.getByLabel("Agent", { exact: true })).toHaveValue(agentId);
    await intake.getByLabel("Idea", { exact: true }).fill(idea);

    const draftRequestPromise = page.waitForRequest((request) =>
      request.method() === "POST" && request.url().endsWith(`/workspaces/${workspace.id}/yixiezuo/drafts`),
    );
    const draftResponsePromise = page.waitForResponse((response) =>
      response.request().method() === "POST" && response.url().endsWith(`/workspaces/${workspace.id}/yixiezuo/drafts`),
    );
    await intake.getByRole("button", { name: "Create requirement", exact: true }).click();
    const draftRequest = await draftRequestPromise;
    const draftResponse = await draftResponsePromise;
    expect(draftResponse.status(), await draftResponse.text()).toBe(201);
    const queued = await draftResponse.json();
    const operationId = queued.id as string;

    // The dialog sends the idea itself and lets the server pick the default
    // template; it must not invent a bot-specific template override.
    expect(draftRequest.postDataJSON()).toEqual({ title, body: idea, agent_id: agentId });
    expect(draftRequest.headers()["idempotency-key"]).toBeTruthy();
    expect(queued).toMatchObject({ kind: "draft", state: "pending", error: "" });
    await expect(intake.getByText("Waiting for the source draft.", { exact: true })).toBeVisible();

    const operation = await claimDraft(api, workspace.id);
    expect(operation).toMatchObject({
      id: operationId,
      kind: "draft",
      state: "running",
      payload: {
        draft: {
          title,
          body: idea,
          agent_id: agentId,
          template: { host: SOURCE_HOST, id: DEFAULT_TEMPLATE_ID },
        },
      },
    });
    expect(operation.lease_token).toBeTruthy();

    // The dialog closes only after the draft read returns the requirement id;
    // that navigation opens the clarifying chat on its own.
    const openResponsePromise = page.waitForResponse((response) =>
      response.request().method() === "POST"
      && new RegExp(`/workspaces/${workspace.id}/requirements/[^/]+/open$`).test(response.url()),
    { timeout: 60_000 });
    const completed = await completeDraft(api, workspace.id, operationId, {
      state: "succeeded",
      lease_token: operation.lease_token,
      snapshot: draftSnapshot(operationId, title, externalId),
    });
    const requirementId = completed.requirement_id as string;
    expect(requirementId).toBeTruthy();
    expect(completed).toMatchObject({ id: operationId, kind: "draft", state: "succeeded" });

    await expect(page).toHaveURL((url) =>
      url.pathname.endsWith("/chat") && url.searchParams.get("requirement") === requirementId,
    { timeout: 30_000 });
    await expect(intake).toBeHidden();
    const openResponse = await openResponsePromise;
    expect(openResponse.status()).toBe(200);
    const opened = await openResponse.json();
    const chatId = opened.session.id as string;
    const planTaskId = opened.pendingTask.task_id as string;
    await expect(page).toHaveURL((url) =>
      url.searchParams.get("session") === chatId && url.searchParams.get("requirement") === requirementId,
    { timeout: 30_000 });

    const requirement = await db.query(
      `SELECT intake_state, chat_session_id::text, intake_operation_id::text, source_host, external_id,
              source_snapshot->>'title' AS title, issue_id::text
       FROM requirement WHERE id = $1`,
      [requirementId],
    );
    expect(requirement.rows[0]).toMatchObject({
      intake_state: "clarifying",
      chat_session_id: chatId,
      intake_operation_id: operationId,
      source_host: SOURCE_HOST,
      external_id: externalId,
      title,
      issue_id: null,
    });
    const planTask = await db.query(
      `SELECT mode, status, requirement_id::text, chat_session_id::text
       FROM agent_task_queue WHERE id = $1`,
      [planTaskId],
    );
    expect(planTask.rows[0]).toMatchObject({
      mode: "plan",
      status: "queued",
      requirement_id: requirementId,
      chat_session_id: chatId,
    });

    // The chat opens on the clarifying surface, not as an ordinary agent chat.
    // The queued plan turn owns the next step, so the rail reports it working.
    await expect(page.getByTestId("requirement-flow-panel")).toBeVisible();
    await expect(page.getByRole("status", { name: "Requirement flow", exact: true })).toContainText("Clarifying");
    await expect(page.getByRole("status", { name: "Requirement flow", exact: true }))
      .toContainText("Working on it — wait before looking for approval.");
    await expect(page.locator('[data-placeholder="Answer the questions above, or ask for a candidate using one of the options"]')
      .first()).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("idea-intake-clarifying-chat.png"), fullPage: true });
  } finally {
    await api.deleteWorkspace();
    await db.end().catch(() => undefined);
  }
});

test("an unconfirmed draft is never created twice from the chat intake", async ({ page }) => {
  test.setTimeout(120_000);
  const api = new TestApiClient();
  const runId = `${Date.now().toString(36)}-${randomUUID().replaceAll("-", "").slice(0, 8)}`;
  const slug = `e2e-idea-unknown-${runId}`;
  const email = `${slug}@multica.ai`;
  const idea = "Remember the email on the login page";
  const db = new pg.Client(DATABASE_URL);
  let draftPosts = 0;

  try {
    const login = await api.login(email, "E2E Idea Unknown User");
    const userId = login.user.id as string;
    const workspace = await api.ensureWorkspace("E2E Idea Unknown", slug);
    await api.markUserOnboarded();
    await db.connect();
    const agentId = await seedAgent(db, { workspaceId: workspace.id, userId, label: runId });

    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token);
      localStorage.setItem("multica:chat:isOpen", "false");
    }, api.getToken()!);
    page.on("request", (request) => {
      if (request.method() === "POST" && request.url().endsWith(`/workspaces/${workspace.id}/yixiezuo/drafts`)) {
        draftPosts += 1;
      }
    });
    await page.goto(`/${workspace.slug}/chat`, { waitUntil: "domcontentloaded" });

    await page.getByRole("button", { name: "New requirement", exact: true }).click();
    const intake = page.getByRole("dialog", { name: "New requirement" });
    await expect(intake.getByLabel("Agent", { exact: true })).toHaveValue(agentId);
    await intake.getByLabel("Idea", { exact: true }).fill(idea);
    const draftResponsePromise = page.waitForResponse((response) =>
      response.request().method() === "POST" && response.url().endsWith(`/workspaces/${workspace.id}/yixiezuo/drafts`),
    );
    await intake.getByRole("button", { name: "Create requirement", exact: true }).click();
    const queued = await (await draftResponsePromise).json();
    const operationId = queued.id as string;

    const operation = await claimDraft(api, workspace.id);
    expect(operation.id).toBe(operationId);
    const completed = await completeDraft(api, workspace.id, operationId, {
      state: "unknown",
      lease_token: operation.lease_token,
      error: "Draft creation could not be confirmed. Do not create it again; inspect the source.",
    });
    expect(completed).toMatchObject({ id: operationId, state: "unknown" });
    expect(completed.requirement_id ?? "").toBe("");

    // Unconfirmed means locked: no new draft, no navigation, only readback.
    await expect(intake.getByText("The draft result is not confirmed. Do not create another one.", { exact: true }))
      .toBeVisible({ timeout: 30_000 });
    await expect(intake.getByText(`[Multica draft ${operationId}]`, { exact: true })).toBeVisible();
    await expect(intake.getByLabel("Idea", { exact: true })).toBeDisabled();
    await expect(intake.getByRole("button", { name: "Create requirement", exact: true })).toBeDisabled();
    await expect(intake.getByLabel("New issue link", { exact: true })).toBeVisible();
    await expect(page).toHaveURL((url) => url.searchParams.get("requirement") === null);
    expect(draftPosts).toBe(1);

    const requirements = await db.query<{ count: number }>(
      `SELECT count(*)::int AS count FROM requirement WHERE workspace_id = $1`,
      [workspace.id],
    );
    expect(requirements.rows[0]!.count).toBe(0);
    const stored = await db.query(
      `SELECT state, requirement_id::text FROM yixiezuo_operation WHERE id = $1`,
      [operationId],
    );
    expect(stored.rows[0]).toEqual({ state: "unknown", requirement_id: null });
  } finally {
    await api.deleteWorkspace();
    await db.end().catch(() => undefined);
  }
});
