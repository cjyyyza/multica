import "./env";

import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { TestApiClient } from "./fixtures";

const enabled = process.env.MULTICA_RUN_SEVEN_ACCEPTANCE === "1";
const evidence = process.env.MULTICA_SEVEN_EVIDENCE_DIR || "";
const baseURL = process.env.NEXT_PUBLIC_API_URL || "";

test.beforeEach(async () => {
  if (!enabled) return;
  expect(evidence).not.toBe("");
  expect(baseURL).toBe("http://127.0.0.1:18980");
  const health = await (await fetch(`${baseURL}/health`)).json();
  expect(health.commit).toBe("4d32633b9-seven-9dddbb2a");
});

test("prepare isolated seven-case acceptance workspace", async () => {
  test.skip(!enabled || process.env.MULTICA_SEVEN_PHASE !== "prepare", "explicit live acceptance setup only");
  const api = new TestApiClient();
  const email = "seven-9dddbb2a@test.local";
  const login = await api.login(email, "Seven-case acceptance");
  const workspace = await api.ensureWorkspace("Seven-case acceptance", "seven-9dddbb2a");
  await api.markUserOnboarded();
  await writeFile(path.join(evidence, "session.json"), JSON.stringify({
    email, token: api.getToken(), user: login.user, workspace, baseURL,
  }, null, 2));
  const initialized = JSON.parse(await readFile(path.join(evidence, "session.json"), "utf8"));
  expect(initialized.workspace.id).toBe(workspace.id);
});

test("live Requirement membership and viewer boundaries", async () => {
  test.skip(!enabled || process.env.MULTICA_SEVEN_PHASE !== "roles", "explicit live acceptance roles only");
  const session = JSON.parse(await readFile(path.join(evidence, "session.json"), "utf8"));
  const state = JSON.parse(await readFile(path.join(evidence, "state.json"), "utf8"));
  const owner = new TestApiClient();
  await owner.login(session.email, "Seven-case acceptance");
  owner.setWorkspaceId(session.workspace.id);
  const actors: Record<string, { id: string; token: string | null }> = {};
  for (const role of ["viewer", "nonparticipant", "outsider"]) {
    const email = `seven-${role}-9dddbb2a@test.local`;
    const client = new TestApiClient();
    const login = await client.login(email, `Seven ${role}`);
    await client.markUserOnboarded();
    if (role !== "outsider") await owner.addMemberByEmail(email);
    actors[role] = { id: login.user.id, token: client.getToken() };
  }
  const route = `/api/workspaces/${session.workspace.id}/requirements/${state.requirementId}`;
  const call = async (token: string | null, suffix = "", body?: unknown) => fetch(`${baseURL}${route}${suffix}`, {
    method: body === undefined ? "GET" : "POST",
    headers: { Authorization: `Bearer ${token}`, "X-Workspace-ID": session.workspace.id, "Content-Type": "application/json", "Idempotency-Key": `seven-roles-${suffix}-9dddbb2a` },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const detail = await (await call(session.token)).json();
  const updated = await call(session.token, "/participants", {
    expectedRevision: detail.revision,
    participants: [{ userId: session.user.id, roles: ["requester", "approver"] }, { userId: actors.viewer.id, roles: ["viewer"] }],
  });
  expect(updated.ok, await updated.text()).toBe(true);
  const viewerRead = await call(actors.viewer.token);
  expect(viewerRead.status).toBe(200);
  const viewerDetail = await viewerRead.json();
  expect(viewerDetail.permissions.canRead).toBe(true);
  for (const permission of ["canApprove", "canAccept", "canReject", "canCancel", "canReconcile", "canManageParticipants", "canSubmitCandidate"]) {
    expect(viewerDetail.permissions[permission], permission).toBe(false);
  }
  const receipts: Record<string, number> = {};
  for (const role of ["nonparticipant", "outsider"]) {
    const response = await call(actors[role].token);
    receipts[`${role}_read`] = response.status;
    expect([403, 404]).toContain(response.status);
  }
  for (const action of ["approve", "accept", "reject", "cancel", "participants"]) {
    const response = await call(actors.viewer.token, `/${action}`, {
      expectedRevision: viewerDetail.revision, expectedIssueRevision: viewerDetail.issue?.revision || 1,
      candidateId: viewerDetail.latestCandidate?.id, candidateDigest: viewerDetail.latestCandidate?.digest,
      reason: "Viewer must never change this Requirement", participants: [],
    });
    receipts[`viewer_${action}`] = response.status;
    expect(response.status, `${action}: ${await response.text()}`).toBe(403);
  }
  await writeFile(path.join(evidence, "role-sessions.json"), JSON.stringify(actors, null, 2));
  await writeFile(path.join(evidence, "case2-permissions.json"), JSON.stringify({ permissions: viewerDetail.permissions, receipts }, null, 2));
});

test("live cancelled Requirement fences its blocked question", async () => {
  test.skip(!enabled || process.env.MULTICA_SEVEN_PHASE !== "cancel", "explicit live cancellation only");
  const { default: pg } = await import("pg");
  const { randomBytes, createHash } = await import("node:crypto");
  const session = JSON.parse(await readFile(path.join(evidence, "session.json"), "utf8"));
  const state = JSON.parse(await readFile(path.join(evidence, "state.json"), "utf8"));
  const owner = new TestApiClient();
  await owner.login(session.email, "Seven-case acceptance");
  owner.setWorkspaceId(session.workspace.id);
  const suffix = randomBytes(4).toString("hex");
  const issue = await owner.createIssue(`【链路验收】cancelled question fixture ${suffix}`, { status: "in_progress" });
  expect(typeof issue.id, JSON.stringify(issue)).toBe("string");
  const db = new pg.Client({ connectionString: process.env.DATABASE_URL });
  await db.connect();
  try {
    const fixtureHost = `seven-cancel-${suffix}.pm.netease.com`;
    const inserted = await db.query(`INSERT INTO requirement
      (workspace_id,source_host,external_id,source_url,source_snapshot,source_digest,requester_id,agent_id,issue_id)
      SELECT workspace_id,$3,'108402',$4,source_snapshot,source_digest,requester_id,agent_id,$2
      FROM requirement WHERE id=$1 RETURNING id`, [state.requirementId, issue.id, fixtureHost, `https://${fixtureHost}/issues/108402`]);
    const id = inserted.rows[0].id;
    await writeFile(path.join(evidence, "cancel-fixture.json"), JSON.stringify({ requirementId: id, issueId: issue.id }, null, 2));
    await db.query(`INSERT INTO requirement_participant(workspace_id,requirement_id,user_id,role)
      VALUES($1,$2,$3,'requester'),($1,$2,$3,'approver')`, [session.workspace.id, id, session.user.id]);
    const task = (await db.query(`INSERT INTO agent_task_queue
      (agent_id,runtime_id,issue_id,requirement_id,mode,status,started_at,priority,originator_user_id,accountable_user_id)
      VALUES($1,$2,$3,$4,'execute','running',now(),2,$5,$5) RETURNING id`, [state.agentId, state.runtimeId, issue.id, id, session.user.id])).rows[0];
    const token = `mat_${randomBytes(32).toString("hex")}`;
    await db.query(`INSERT INTO task_token(token_hash,task_id,agent_id,workspace_id,user_id,expires_at)
      VALUES($1,$2,$3,$4,$5,now()+interval '1 hour')`, [createHash("sha256").update(token).digest("hex"), task.id, state.agentId, session.workspace.id, session.user.id]);
    const route = `/api/workspaces/${session.workspace.id}/requirements/${id}`;
    const call = async (url: string, body?: unknown, actor = session.token, key = "") => {
      const response = await fetch(`${baseURL}${url}`, {
        method: body === undefined ? "GET" : "POST",
        headers: { Authorization: `Bearer ${actor}`, "X-Workspace-ID": session.workspace.id, "Content-Type": "application/json", ...(key ? { "Idempotency-Key": key } : {}) },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const result = await response.json();
      expect(response.ok, JSON.stringify(result)).toBe(true);
      return result;
    };
    const before = await call(route);
    const blocked = await call(`${route}/block`, { question: "【链路验收】cancel must fence this question", targetUserId: session.user.id, expectedIssueRevision: before.issue.revision }, token, `seven-cancel-block-${id}`);
    await call(`/api/daemon/tasks/${task.id}/complete`, { output: "Controlled fixture: cancelled acceptance task." });
    const waiting = await call(route);
    const cancelled = await call(`${route}/cancel`, { expectedIssueRevision: waiting.issue.revision, reason: "Acceptance cancellation only" }, session.token, `seven-cancel-${id}`);
    expect(cancelled.state).toBe("cancelled");
    await owner.createComment(issue.id, "【链路验收】late answer after cancellation must not restart work");
    await new Promise(resolve => setTimeout(resolve, 12_000));
    const wakeup = (await db.query("SELECT enabled,disabled_at FROM issue_wakeup WHERE id=$1", [blocked.wakeup.id])).rows[0];
    expect(wakeup.enabled).toBe(false);
    expect(wakeup.disabled_at).toBeTruthy();
    const tasks = (await db.query("SELECT id,status FROM agent_task_queue WHERE requirement_id=$1", [id])).rows;
    expect(tasks).toHaveLength(1);
    expect((await call(route)).state).toBe("cancelled");
    await writeFile(path.join(evidence, "case4-cancelled.json"), JSON.stringify({ driver: "TestApiClient issue plus explicitly declared Requirement/task fixtures; actual Docker block/cancel/comment APIs and worker", requirementId: id, issueId: issue.id, wakeup, tasks, state: "cancelled" }, null, 2));
  } finally {
    await db.end();
  }
});

test("prepare the second native desktop workspace", async () => {
  test.skip(!enabled || process.env.MULTICA_SEVEN_PHASE !== "desktop-extra", "explicit native workspace setup only");
  const session = JSON.parse(await readFile(path.join(evidence, "session.json"), "utf8"));
  const owner = new TestApiClient();
  await owner.login(session.email, "Seven-case acceptance");
  const slug = "seven-other-9dddbb2a";
  const workspace = (await owner.getWorkspaces()).find(ws => ws.slug === slug)
    || await owner.createWorkspace("Seven second workspace", slug);
  await writeFile(path.join(evidence, "second-workspace.json"), JSON.stringify(workspace, null, 2));
});
