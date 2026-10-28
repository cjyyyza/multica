import { test, expect } from "@playwright/test";
import { createHash } from "node:crypto";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import pg from "pg";
import { TestApiClient } from "./fixtures";

const enabled = process.env.MULTICA_RUN_REAL_AGENT_SMOKE === "1" && process.env.MULTICA_DSH_REQUIREMENT_LIVE === "1";
const phase = process.env.MULTICA_DSH_REQUIREMENT_PHASE;
const evidence = process.env.MULTICA_DSH_EVIDENCE ?? "";
const apiURL = process.env.NEXT_PUBLIC_API_URL ?? "";
const appURL = process.env.PLAYWRIGHT_BASE_URL ?? "";
const filename = "requirement-dsh-acceptance.md";
const marker = "DSH_REQUIREMENT_PLAN_EXECUTE_20261028";

async function save(name: string, value: unknown) {
  await writeFile(path.join(evidence, name), JSON.stringify(value, null, 2), { mode: 0o600 });
}

async function filesNamed(root: string, name: string): Promise<string[]> {
  const found: string[] = [];
  for (const item of await readdir(root, { withFileTypes: true }).catch(() => [])) {
    const file = path.join(root, item.name);
    if (item.isDirectory() && ![".git", "node_modules"].includes(item.name)) found.push(...await filesNamed(file, name));
    else if (item.isFile() && item.name === name) found.push(file);
  }
  return found;
}

test("prepare isolated member, workspace and daemon credentials", async () => {
  test.skip(!enabled || phase !== "bootstrap", "explicit real DSH acceptance only");
  expect(evidence).not.toBe("");
  expect(new URL(apiURL).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  expect((await (await fetch(`${apiURL}/health`)).json()).commit).toBe("dsh-requirement-live-20261028");
  await mkdir(evidence, { recursive: true });
  const nonce = `${Date.now().toString(36)}-${process.pid}`;
  const api = new TestApiClient();
  const email = `dsh-requirement-${nonce}@test.local`;
  const login = await api.login(email, "DSH requirement acceptance");
  const workspace = await api.createWorkspace("DSH requirement acceptance", `dsh-requirement-${nonce}`);
  api.setWorkspaceId(workspace.id);
  await api.markUserOnboarded();
  const response = await fetch(`${apiURL}/api/tokens`, {
    method: "POST", headers: { Authorization: `Bearer ${api.getToken()}`, "Content-Type": "application/json" },
    body: JSON.stringify({ name: `DSH acceptance ${nonce}`, expires_in_days: 1 }),
  });
  expect(response.status).toBe(201);
  const pat = await response.json();
  const profile = `dsh-requirement-${nonce}`;
  const profileDirectory = path.join(os.homedir(), ".multica", "profiles", profile);
  await mkdir(profileDirectory, { recursive: true });
  await writeFile(path.join(profileDirectory, "config.json"), JSON.stringify({
    server_url: apiURL, app_url: appURL, token: pat.token, workspace_id: workspace.id,
    device_name: "DSH requirement acceptance only", runtime_name: "DSH requirement acceptance",
    workspaces_root: path.join(evidence, "workspaces"), max_concurrent_tasks: 1,
    poll_interval: "1s", heartbeat_interval: "5s", agent_timeout: "5m", disable_auto_update: true,
  }, null, 2), { mode: 0o600 });
  await save("session.json", {
    apiURL, appURL, email, token: api.getToken(), daemonTokenId: pat.id,
    profile, profileDirectory, workspace, user: login.user,
  });
});

test("real DSH Plan, browser approval, and Execute", async ({ page, context }) => {
  test.skip(!enabled || phase !== "flow", "explicit real DSH acceptance only");
  test.setTimeout(660_000);
  page.setDefaultTimeout(45_000);
  const session = JSON.parse(await readFile(path.join(evidence, "session.json"), "utf8"));
  expect(session.apiURL).toBe(apiURL);
  const headers = { Authorization: `Bearer ${session.token}`, "X-Workspace-ID": session.workspace.id, "Content-Type": "application/json" };
  const call = async (route: string, body?: unknown, key?: string) => {
    const response = await fetch(`${apiURL}${route}`, {
      method: body === undefined ? "GET" : "POST",
      headers: { ...headers, ...(key ? { "Idempotency-Key": key } : {}) },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const value = await response.json();
    expect(response.ok, JSON.stringify(value)).toBe(true);
    return value;
  };
  const runtimeList = await call("/api/runtimes");
  const runtime = runtimeList.find((item: any) => item.provider === "dsh" && item.status === "online");
  expect(runtime?.id, JSON.stringify(runtimeList)).toBeTruthy();
  const model = process.env.MULTICA_DSH_REQUIREMENT_MODEL;
  expect(model, "Select an ID from the installed DSH model catalog").toBeTruthy();
  const request = `This is an isolated integration acceptance with a complete, final requirement. After approval in Multica Chat, create a UTF-8 file named ${filename} in the current task working directory. It must contain ${marker} and a concise description of Plan, human approval, and Execute. Compute the file's SHA256 using an available local command and report its absolute path and SHA256 in the final reply. Do not change an existing repository, install packages, commit, contact people, or call POPO/Yixiezuo. Before approval, only plan and hand back a complete candidate through the requested structured output; do not create the file. No clarification is needed. Project is null; use the exact agentId in the captured requirement.`;
  const agent = await call("/api/agents", {
    name: `DSH requirement acceptance ${Date.now()}`, runtime_id: runtime.id, permission_mode: "private",
    max_concurrent_tasks: 1, model, thinking_level: "off",
    instructions: "Only perform the isolated requirement acceptance described by its source and candidate. Follow the current Plan or Execute mode and the Multica approval boundary. The runtime is headless; use its structured output for clarification or candidate handoff.",
  });
  const db = new pg.Client(process.env.DATABASE_URL);
  await db.connect();
  let requirementId = "";
  try {
    const externalId = Date.now().toString();
    const source = { host: "dj01.pm.netease.com", id: externalId, url: `https://dj01.pm.netease.com/issues/${externalId}` };
    const snapshot = {
      source, title: `DSH Plan approval Execute acceptance ${externalId}`, description: request,
      status: "open", priority: "normal", updated_at: "", lock_version: 3,
      attachments: [], comments: [], statuses: [], warnings: [], digest: "", fields: [],
    };
    const digest = createHash("sha256").update(JSON.stringify({ ...snapshot, attachments: null, statuses: null, warnings: null })).digest("hex");
    snapshot.digest = digest;
    // Only the source readback is a fixture; task claims, model turns and approval are real.
    const operation = await db.query(
      `INSERT INTO yixiezuo_operation (workspace_id, requested_by, kind, state, payload, result)
       VALUES ($1, $2, 'preview', 'succeeded', $3::jsonb, $4::jsonb) RETURNING id::text`,
      [session.workspace.id, session.user.id, JSON.stringify({ source }), JSON.stringify(snapshot)],
    );
    const requirement = await call(`/api/workspaces/${session.workspace.id}/requirements`, {
      sourceHost: source.host, externalId, sourceUrl: source.url, sourceSnapshot: snapshot,
      sourceDigest: digest, sourceLockVersion: 3, agentId: agent.id, intakeOperationId: operation.rows[0].id,
    }, `dsh-create-${externalId}`);
    requirementId = requirement.id;
    const route = `/api/workspaces/${session.workspace.id}/requirements/${requirementId}`;
    const opened = await call(`${route}/open`, { expectedRevision: requirement.revision }, `dsh-open-${externalId}`);
    await save("flow-state.json", { requirementId, agentId: agent.id, runtimeId: runtime.id, chatId: opened.session.id, planTaskId: opened.pendingTask.task_id });
    await context.addCookies([{ name: "multica-locale", value: "en", url: appURL }]);
    await page.goto(`${appURL}/login`);
    await page.locator('input[type="email"]').fill(session.email);
    await page.locator('button[form="login-form"]').click();
    await page.locator('input[autocomplete="one-time-code"]').fill("888888");
    await expect(page).not.toHaveURL(/\/login/, { timeout: 60_000 });
    await page.goto(`${appURL}/${session.workspace.slug}/chat?session=${opened.session.id}&requirement=${requirementId}`);
    const panel = page.getByTestId("requirement-flow-panel").filter({ visible: true }).first();
    await expect(panel).toBeVisible({ timeout: 60_000 });
    const getDetail = () => call(route);
    const tasks = async () => (await db.query(
      `SELECT id::text, mode, status, session_id, result FROM agent_task_queue WHERE requirement_id=$1 ORDER BY created_at`, [requirementId],
    )).rows;
    const waitState = async (wanted: string) => {
      await expect.poll(async () => {
        const detail = await getDetail();
        const runs = await tasks();
        const failed = runs.find((run: any) => ["failed", "cancelled", "blocked"].includes(run.status));
        if (failed || detail.state === "blocked") throw new Error(`Requirement ${detail.state}: ${JSON.stringify({ runs, run: detail.run })}`);
        return detail.state;
      }, { timeout: 300_000, intervals: [1000, 2000] }).toBe(wanted);
    };
    await waitState("awaiting_approval");
    const planned = await getDetail();
    expect(planned.issueId).toBeNull();
    expect(JSON.stringify(planned.latestCandidate.snapshot)).toContain(filename);
    expect(await filesNamed(path.join(evidence, "workspaces"), filename)).toEqual([]);
    await save("plan-detail.json", planned);
    await panel.getByRole("button", { name: "Review and approve", exact: true }).click();
    const approval = page.getByRole("alertdialog");
    await expect(approval.getByRole("button", { name: "Create Issue and start agent", exact: true })).toBeEnabled();
    await page.screenshot({ path: path.join(evidence, "plan-approval.png"), fullPage: true });
    const approvalResponse = page.waitForResponse(response => response.request().method() === "POST" && response.url().endsWith(`${route}/approve`));
    await approval.getByRole("button", { name: "Create Issue and start agent", exact: true }).click();
    expect((await approvalResponse).status()).toBe(200);
    await waitState("in_review");
    const review = await getDetail();
    const files = await filesNamed(path.join(evidence, "workspaces"), filename);
    expect(files).toHaveLength(1);
    const bytes = await readFile(files[0]);
    expect(bytes.toString("utf8")).toContain(marker);
    const sha256 = createHash("sha256").update(bytes).digest("hex");
    expect(String(review.run?.result?.output).toLowerCase()).toContain(sha256);
    const runs = await tasks();
    expect(runs.map((run: any) => [run.mode, run.status])).toEqual([["plan", "completed"], ["execute", "completed"]]);
    const counts = (await db.query(
      `SELECT (SELECT count(*) FROM requirement_approval WHERE requirement_id=$1)::int AS approvals,
       (SELECT count(*) FROM issue WHERE origin_type='requirement_chat' AND origin_id=$2)::int AS issues`,
      [requirementId, opened.session.id],
    )).rows[0];
    expect(counts).toEqual({ approvals: 1, issues: 1 });
    await save("execute-detail.json", review);
    await save("receipt.json", {
      requirementId, workspace: session.workspace, agentId: agent.id, runtimeId: runtime.id, provider: runtime.provider, model,
      candidateId: planned.latestCandidate.id, issueId: review.issueId, counts, runs,
      artifact: { path: files[0], sha256, bytes: bytes.length }, approval: "Multica Chat browser confirmation", source: "synthetic preview fixture; no external writes",
    });
    await page.screenshot({ path: path.join(evidence, "execute-in-review.png"), fullPage: true });
  } catch (error) {
    await save("error.json", { requirementId, error: String(error), url: page.url(), body: await page.locator("body").innerText().catch(() => "") });
    await page.screenshot({ path: path.join(evidence, "error.png"), fullPage: true }).catch(() => {});
    throw error;
  } finally {
    await db.end();
  }
});
