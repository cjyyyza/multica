import "./env";
import { mkdir, readFile, writeFile, readdir } from "node:fs/promises";
import { createHash } from "node:crypto";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { TestApiClient } from "./fixtures";

const enabled = process.env.MULTICA_RUN_REQUIREMENT_NINE === "1";
const evidence = process.env.MULTICA_NINE_EVIDENCE_DIR || "";
const baseURL = "http://127.0.0.1:18208";
const flowEvidence = process.env.MULTICA_NINE_FLOW_CASE ? path.join(evidence, process.env.MULTICA_NINE_FLOW_CASE) : evidence;

test("prepare the isolated requirement acceptance account", async () => {
  test.skip(!enabled || process.env.MULTICA_NINE_PHASE !== "prepare", "explicit live acceptance setup only");
  expect(process.env.NEXT_PUBLIC_API_URL).toBe(baseURL);
  expect(process.env.DATABASE_URL).toContain("/multica_requirement_nine_128?");
  expect(evidence).not.toBe("");
  const health = await (await fetch(`${baseURL}/health`)).json();
  expect(health.commit).toBe("4d32633b9-nine-20261027");
  await mkdir(evidence, { recursive: true });
  const sessionPath = path.join(evidence, "session.json");
  try {
    const existing = JSON.parse(await readFile(sessionPath, "utf8"));
    expect(existing.baseURL).toBe(baseURL);
    return;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
  }
  const api = new TestApiClient();
  const email = "requirement-nine-20261027@test.local";
  const login = await api.login(email, "Requirement acceptance");
  const slug = "requirement-nine-20261027";
  const workspace = (await api.getWorkspaces()).find((item) => item.slug === slug)
    ?? await api.createWorkspace("Requirement acceptance", slug);
  api.setWorkspaceId(workspace.id);
  await api.markUserOnboarded();
  const token = api.getToken();
  const tokenHeaders = { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
  const existingTokens = await (await fetch(`${baseURL}/api/tokens`, { headers: tokenHeaders })).json();
  for (const previous of existingTokens) {
    if (previous.name === "Requirement acceptance daemon") {
      expect((await fetch(`${baseURL}/api/tokens/${previous.id}`, { method: "DELETE", headers: tokenHeaders })).ok).toBe(true);
    }
  }
  const patResponse = await fetch(`${baseURL}/api/tokens`, {
    method: "POST", headers: tokenHeaders,
    body: JSON.stringify({ name: "Requirement acceptance daemon", expires_in_days: 1 }),
  });
  expect(patResponse.status).toBe(201);
  const pat = await patResponse.json();
  expect(typeof pat.token === "string" && pat.token.startsWith("mul_")).toBe(true);
  await writeFile(sessionPath, JSON.stringify({
    baseURL, appURL: "http://127.0.0.1:13128", email, token, daemonToken: pat.token,
    daemonTokenId: pat.id, user: login.user, workspace,
  }, null, 2), { mode: 0o600 });
  await writeFile(path.join(evidence, "daemon-config.json"), JSON.stringify({
    server_url: baseURL, app_url: "http://127.0.0.1:13128", token: pat.token,
    workspace_id: workspace.id, device_name: "Requirement acceptance only",
    runtime_name: "Requirement acceptance Claude", workspaces_root: path.join(evidence, "workspaces"),
    max_concurrent_tasks: 1, poll_interval: "1s", heartbeat_interval: "5s",
  }, null, 2), { mode: 0o600 });
});

test("prepare an agent on the real isolated Claude runtime", async () => {
  test.skip(!enabled || process.env.MULTICA_NINE_PHASE !== "agent", "explicit real runtime acceptance only");
  expect(process.env.NEXT_PUBLIC_API_URL).toBe(baseURL);
  expect(process.env.DATABASE_URL).toContain("/multica_requirement_nine_128?");
  const sessionPath = path.join(evidence, "session.json");
  const session = JSON.parse(await readFile(sessionPath, "utf8"));
  expect(session.baseURL).toBe(baseURL);
  const api = new TestApiClient();
  await api.login(session.email, "Requirement acceptance");
  api.setWorkspaceId(session.workspace.id);
  api.setWorkspaceSlug(session.workspace.slug);
  session.token = api.getToken();
  const headers = { Authorization: `Bearer ${api.getToken()}`, "X-Workspace-ID": session.workspace.id, "Content-Type": "application/json" };
  const runtimes = await (await fetch(`${baseURL}/api/runtimes`, { headers })).json();
  const runtime = runtimes.find((item: { provider: string; status: string }) => item.provider === "claude" && item.status === "online");
  expect(Boolean(runtime?.id)).toBe(true);
  const agents = await (await fetch(`${baseURL}/api/agents`, { headers })).json();
  const name = "Requirement nine live acceptance";
  let agent = agents.find((item: { name: string }) => item.name === name);
  if (!agent) {
    const response = await fetch(`${baseURL}/api/agents`, {
      method: "POST", headers,
      body: JSON.stringify({ name, runtime_id: runtime.id, permission_mode: "private", max_concurrent_tasks: 1,
        description: "仅用于隔离 Requirement 链路验收。",
        instructions: "This agent is only for isolated Requirement acceptance. Follow the Requirement mode: Plan is read-only and must submit controlled structured output; never create files before human approval. Execute may create only the temporary acceptance files requested in its candidate. Never change repository files, contact people, or write to external systems directly. The host owns reviewed source publication. Always distinguish acceptance artifacts from real business completion.",
      }),
    });
    expect(response.status).toBe(201);
    agent = await response.json();
  }
  expect(agent.runtime_id).toBe(runtime.id);
  await writeFile(sessionPath, JSON.stringify(session, null, 2), { mode: 0o600 });
  await writeFile(path.join(evidence, "agent-live.json"), JSON.stringify({ id: agent.id, name: agent.name, runtime_id: runtime.id, prepared_at: new Date().toISOString() }, null, 2));
});

test("configure the real POPO bridge for isolated natural-language intake", async () => {
  test.skip(!enabled || process.env.MULTICA_NINE_PHASE !== "bridge", "explicit live bridge acceptance only");
  expect(process.env.NEXT_PUBLIC_API_URL).toBe(baseURL);
  expect(process.env.DATABASE_URL).toContain("/multica_requirement_nine_128?");
  const sessionPath = path.join(evidence, "session.json");
  const session = JSON.parse(await readFile(sessionPath, "utf8"));
  const agent = JSON.parse(await readFile(path.join(evidence, "agent-live.json"), "utf8"));
  const bridge = JSON.parse(await readFile(path.join(evidence, "bridge-nine", "config.json"), "utf8"));
  expect(bridge.server).toBe(baseURL);
  const health = await (await fetch(`${baseURL}/health`)).json();
  expect(health.commit).toBe("4d32633b9-nine-draft-20261027");
  const api = new TestApiClient();
  await api.login(session.email, "Requirement acceptance");
  api.setWorkspaceId(session.workspace.id);
  session.token = api.getToken();
  const headers = { Authorization: `Bearer ${api.getToken()}`, "X-Workspace-ID": session.workspace.id, "Content-Type": "application/json" };
  const installations = await (await fetch(`${baseURL}/api/workspaces/${session.workspace.id}/popo/installations`, { headers })).json();
  let installation = installations.installations.find((item: { robot_id: string; status: string }) => item.robot_id === "popo-ws-b8582eff" && item.status === "active");
  if (!installation) {
    const response = await fetch(`${baseURL}/api/workspaces/${session.workspace.id}/popo/install?agent_id=${agent.id}`, {
      method: "POST", headers, body: JSON.stringify({ bridge_id: bridge.bridge_id, robot_id: "popo-ws-b8582eff", robot_name: "Requirement acceptance robot" }),
    });
    const body = await response.json();
    expect(response.status, JSON.stringify(body)).toBe(200);
    installation = body;
  }
  expect(installation.agent_id).toBe(agent.id);
  const response = await fetch(`${baseURL}/api/workspaces/${session.workspace.id}/popo/installations/${installation.id}/requirement-intake`, {
    method: "PATCH", headers, body: JSON.stringify({ template_url: "https://dj01.pm.netease.com/issues/108402" }),
  });
  expect(response.status).toBe(200);
  const bindingId = await api.seedPopoBinding(installation.id, "yujian01@corp.netease.com");
  expect((await fetch(`${baseURL}/api/me`, { method: "PATCH", headers, body: JSON.stringify({ language: "en" }) })).ok).toBe(true);
  await writeFile(sessionPath, JSON.stringify(session, null, 2), { mode: 0o600 });
  await writeFile(path.join(evidence, "intake-bridge.json"), JSON.stringify({
    installationId: installation.id, bridgeId: bridge.bridge_id, agentId: agent.id, bindingId,
    identitySetup: "TestApiClient seeds the agreed POPO user only in the isolated DB; this case does not verify account binding.",
  }, null, 2));
});

async function acceptanceArtifacts(root: string, filename = "requirement-nine-acceptance.md"): Promise<string[]> {
  const found: string[] = [];
  for (const item of await readdir(root, { withFileTypes: true }).catch(() => [])) {
    const file = path.join(root, item.name);
    if (item.isDirectory() && ![".git", "node_modules"].includes(item.name)) found.push(...await acceptanceArtifacts(file, filename));
    else if (item.isFile() && item.name === filename) found.push(file);
  }
  return found;
}

test("real Claude Plan, web Chat approval, execution and reviewed source publication", async ({ page, context }) => {
  test.skip(!enabled || process.env.MULTICA_NINE_PHASE !== "flow", "explicit live agent and source acceptance only");
  test.setTimeout(900_000);
  page.setDefaultTimeout(30_000);
  expect(process.env.NEXT_PUBLIC_API_URL).toBe(baseURL);
  expect(process.env.DATABASE_URL).toContain("/multica_requirement_nine_128?");
  const filename = process.env.MULTICA_NINE_ARTIFACT || "requirement-nine-acceptance.md";
  expect(filename).toMatch(/^requirement-nine-[a-z-]+\.md$/);
  await mkdir(flowEvidence, { recursive: true });
  const session = JSON.parse(await readFile(path.join(evidence, "session.json"), "utf8"));
  const operation = JSON.parse(await readFile(path.join(evidence, process.env.MULTICA_NINE_OPERATION_FILE || "draft-fixed-result.json"), "utf8"));
  expect(operation.state).toBe("succeeded");
  expect(Boolean(operation.requirement_id)).toBe(true);
  const requirementId = operation.requirement_id;
  const headers = { Authorization: `Bearer ${session.token}`, "X-Workspace-ID": session.workspace.id };
  const getDetail = async () => {
    const response = await fetch(`${baseURL}/api/workspaces/${session.workspace.id}/requirements/${requirementId}`, { headers });
    expect(response.ok).toBe(true);
    return response.json();
  };
  const artifactRoot = path.join(evidence, "workspaces");
  const state: Record<string, unknown> = { requirementId, operationId: operation.id, source: operation.payload.source, agentId: operation.payload.draft.agent_id };
  try {
    await context.addCookies([{ name: "multica-locale", value: "en", url: session.appURL }]);
    await page.goto(`${session.appURL}/login`);
    await page.locator('input[type="email"]').fill(session.email);
    await page.locator('button[form="login-form"]').click();
    await page.locator('input[autocomplete="one-time-code"]').fill("888888");
    await expect(page).not.toHaveURL(/\/login/, { timeout: 30_000 });
    expect((await context.cookies(session.appURL)).some(cookie => cookie.name === "multica_auth")).toBe(true);
    await page.goto(`${session.appURL}/${session.workspace.slug}/chat?requirement=${requirementId}`);
    const panel = page.getByTestId("requirement-flow-panel").filter({ visible: true }).first();
    await expect(panel).toBeVisible({ timeout: 30_000 });
    const initial = await getDetail();
    if (!initial.issueId) {
    if (process.env.MULTICA_NINE_RETRY_PLAN === "1") {
      expect((await getDetail()).state).toBe("clarifying");
      await page.locator('.ProseMirror[contenteditable="true"]').filter({ visible: true }).last().fill(
        `${operation.payload.draft.body}。请提交完整候选供我在 Multica Chat 批准，批准前不要生成文件。`,
      );
      await page.getByRole("button", { name: "Send", exact: true }).click();
    }
    await expect.poll(async () => (await getDetail()).state, { timeout: 330_000, intervals: [1000, 2000] }).toBe("awaiting_approval");
    const planned = await getDetail();
    expect(JSON.stringify(planned.latestCandidate.snapshot)).toContain(filename);
    expect(planned.issueId).toBeNull();
    expect(await acceptanceArtifacts(artifactRoot, filename)).toEqual([]);
    await writeFile(path.join(flowEvidence, "live-plan-detail.json"), JSON.stringify(planned, null, 2));
    await panel.getByRole("button", { name: "Review and approve", exact: true }).click();
    const approval = page.getByRole("alertdialog");
    await expect(approval.getByText("Approve and create Issue", { exact: true })).toBeVisible();
    await page.screenshot({ path: path.join(flowEvidence, "web-live-candidate-review.png") });
    await approval.getByRole("button", { name: "Create Issue and start agent", exact: true }).click();
    await expect.poll(async () => (await getDetail()).issueId, { timeout: 30_000 }).toBeTruthy();
    } else if (initial.state === "blocked") {
      expect(process.env.MULTICA_NINE_RESUME_BLOCKED).toBe("1");
      const question = initial.deliveries.findLast((item: { kind: string }) => item.kind === "requirement_blocked");
      expect(question?.occurrenceId).toBeTruthy();
      await page.goto(`${session.appURL}/${session.workspace.slug}/issues/${initial.issue.identifier}#comment-${question.occurrenceId}`);
      await expect(page.locator(`#comment-body-${question.occurrenceId}`)).toBeVisible({ timeout: 30_000 });
      const questionCard = page.locator('[data-slot="card"]').filter({ has: page.locator(`#comment-body-${question.occurrenceId}`) }).first();
      await questionCard.getByTestId("reply-composer-shell").click();
      const replyEditor = questionCard.locator('.ProseMirror[contenteditable="true"]').filter({ visible: true });
      await expect(replyEditor).toBeVisible();
      await replyEditor.fill(
        `该候选已经在 Multica Chat 批准并创建当前 Issue，无需重复批准。继续按原候选执行：仅在当前临时工作目录生成 ${filename}，包含验收标记和验证步骤，报告路径和 SHA256；不改仓库，不访问生产，不调用易协作或 POPO。`,
      );
      await questionCard.getByRole("button", { name: "Send", exact: true }).click();
      state.resumedQuestionCommentId = question.occurrenceId;
    }
    await expect.poll(async () => (await getDetail()).state, { timeout: 330_000, intervals: [1000, 2000] }).toBe("in_review");
    const review = await getDetail();
    state.issueId = review.issueId;
    state.chatSessionId = review.chatSessionId;
    const files = await acceptanceArtifacts(artifactRoot, filename);
    expect(files).toHaveLength(1);
    const bytes = await readFile(files[0]!);
    expect(bytes.toString("utf8")).toMatch(/验收|acceptance/i);
    state.artifact = { path: files[0], sha256: createHash("sha256").update(bytes).digest("hex"), bytes: bytes.length };
    expect(String(review.run?.result?.output).toLowerCase()).toContain((state.artifact as { sha256: string }).sha256);
    await writeFile(path.join(flowEvidence, "live-execute-detail.json"), JSON.stringify(review, null, 2));
    if (await panel.getByText("Open Issue", { exact: true }).isVisible()) await panel.getByText("Open Issue", { exact: true }).click();
    if (!String(review.run?.result?.output).includes(files[0]!)) {
      await expect(page.locator('[data-comment-content], [id^="comment-body-"]').filter({ hasText: files[0]! }).filter({ visible: true }).first()).toBeVisible();
    }
    const issuePanel = page.getByTestId("requirement-flow-panel").filter({ visible: true }).first();
    await issuePanel.getByRole("button", { name: "Accept", exact: true }).click();
    const accept = page.getByRole("alertdialog");
    const summary = accept.getByLabel("Delivery summary and links", { exact: true });
    await expect.poll(() => summary.inputValue()).toContain(filename);
    const reviewedSummary = `【Multica链路验收】真实 Plan 与 Execute 已验证。临时文件：${filename}\nSHA256: ${(state.artifact as { sha256: string }).sha256}\n仅验证协作链路，不代表108402业务完成。`;
    await summary.fill(reviewedSummary);
    await accept.getByLabel("Yixiezuo status", { exact: true }).selectOption(review.sourceSnapshot.status);
    await page.screenshot({ path: path.join(flowEvidence, "web-live-delivery-review.png") });
    await accept.getByRole("button", { name: "Accept and publish", exact: true }).click();
    await expect.poll(async () => (await getDetail()).state, { timeout: 30_000 }).toBe("done");
    await expect.poll(async () => {
      const detail = await getDetail();
      return detail.operations.find((item: { kind: string }) => item.kind === "publish")?.state;
    }, { timeout: 120_000, intervals: [1000, 3000] }).toBe("succeeded");
    const done = await getDetail();
    state.finishedAt = new Date().toISOString();
    await writeFile(path.join(flowEvidence, "state.json"), JSON.stringify(state, null, 2));
    await writeFile(path.join(flowEvidence, "live-done-detail.json"), JSON.stringify(done, null, 2));
    await page.screenshot({ path: path.join(flowEvidence, "web-live-done.png") });
  } catch (error) {
    await writeFile(path.join(flowEvidence, "state.json"), JSON.stringify(state, null, 2));
    await writeFile(path.join(flowEvidence, "web-live-error.txt"), `${String(error)}\n\n${await page.locator("body").innerText()}`);
    await page.screenshot({ path: path.join(flowEvidence, "web-live-error.png") }).catch(() => {});
    throw error;
  }
});

test("live owner, viewer and workspace isolation boundaries", async ({ page, context }) => {
  test.skip(!enabled || process.env.MULTICA_NINE_PHASE !== "roles", "explicit isolated client acceptance only");
  expect(process.env.NEXT_PUBLIC_API_URL).toBe(baseURL);
  expect(process.env.DATABASE_URL).toContain("/multica_requirement_nine_128?");
  const session = JSON.parse(await readFile(path.join(evidence, "session.json"), "utf8"));
  const state = JSON.parse(await readFile(path.join(flowEvidence, "state.json"), "utf8"));
  const owner = new TestApiClient();
  await owner.login(session.email, "Requirement acceptance");
  owner.setWorkspaceId(session.workspace.id);
  const actors: Record<string, { id: string; email: string; token: string | null }> = {};
  const roleRun = Date.now().toString(36);
  for (const role of ["viewer", "nonparticipant", "outsider"]) {
    const email = `nine-${role}-${roleRun}@test.local`;
    const client = new TestApiClient();
    const login = await client.login(email, `Nine ${role}`);
    await client.markUserOnboarded();
    if (role !== "outsider") await owner.addMemberByEmail(email);
    actors[role] = { id: login.user.id, email, token: client.getToken() };
  }
  const route = `/api/workspaces/${session.workspace.id}/requirements/${state.requirementId}`;
  const call = (token: string | null, suffix = "", body?: unknown) => fetch(`${baseURL}${route}${suffix}`, {
    method: body === undefined ? "GET" : "POST",
    headers: { Authorization: `Bearer ${token}`, "X-Workspace-ID": session.workspace.id, "Content-Type": "application/json", "Idempotency-Key": `nine-roles-${suffix}-${roleRun}` },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const detail = await (await call(owner.getToken())).json();
  expect(detail.state).toBe("in_review");
  expect(detail.permissions.canAccept).toBe(true);
  const participants = await call(owner.getToken(), "/participants", {
    expectedRevision: detail.revision,
    participants: [{ userId: session.user.id, roles: ["requester", "approver"] }, { userId: actors.viewer!.id, roles: ["viewer"] }],
  });
  expect(participants.ok, await participants.text()).toBe(true);
  const response = await call(actors.viewer!.token);
  expect(response.status).toBe(200);
  const viewer = await response.json();
  expect(viewer.permissions.canRead).toBe(true);
  for (const permission of ["canApprove", "canAccept", "canReject", "canCancel", "canReconcile", "canManageParticipants", "canSubmitCandidate"]) {
    expect(viewer.permissions[permission], permission).toBe(false);
  }
  const receipts: Record<string, number> = {};
  for (const role of ["nonparticipant", "outsider"]) {
    const response = await call(actors[role]!.token);
    receipts[`${role}_read`] = response.status;
    expect([403, 404]).toContain(response.status);
  }
  for (const action of ["approve", "accept", "reject", "cancel", "participants"]) {
    const response = await call(actors.viewer!.token, `/${action}`, {
      expectedRevision: viewer.revision, expectedIssueRevision: viewer.issue?.revision || 1,
      candidateId: viewer.latestCandidate?.id, candidateDigest: viewer.latestCandidate?.digest,
      reason: "Viewer cannot change this Requirement", participants: [],
    });
    receipts[`viewer_${action}`] = response.status;
    if (action === "approve") {
      expect(response.status).toBe(409);
      expect(await response.json()).toMatchObject({ code: "requirement_issue_already_exists" });
    } else {
      expect(response.status, `${action}: ${await response.text()}`).toBe(403);
    }
  }
  const unchanged = await (await call(owner.getToken())).json();
  expect(unchanged.issueId).toBe(detail.issueId);
  expect(unchanged.state).toBe("in_review");
  await context.addCookies([{ name: "multica-locale", value: "en", url: session.appURL }]);
  await page.goto(`${session.appURL}/login`);
  await page.locator('input[type="email"]').fill(actors.viewer!.email);
  await page.locator('button[form="login-form"]').click();
  await page.locator('input[autocomplete="one-time-code"]').fill("888888");
  await expect(page).not.toHaveURL(/\/login/, { timeout: 30_000 });
  await page.goto(`${session.appURL}/${session.workspace.slug}/issues/${detail.issue.identifier}`);
  const panel = page.getByTestId("requirement-flow-panel").filter({ visible: true }).first();
  await expect(panel).toBeVisible({ timeout: 30_000 });
  for (const action of ["Review and approve", "Accept", "Request changes", "Cancel requirement"]) {
    await expect(panel.getByRole("button", { name: action, exact: true })).toHaveCount(0);
  }
  await page.screenshot({ path: path.join(evidence, "web-live-viewer.png") });
  await writeFile(path.join(evidence, "role-sessions.json"), JSON.stringify(actors, null, 2), { mode: 0o600 });
  await writeFile(path.join(evidence, "live-permissions.json"), JSON.stringify({ permissions: viewer.permissions, receipts, viewerUI: true }, null, 2));
});
