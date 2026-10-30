import "./env";
import { randomUUID } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";
import pg from "pg";
import { TestApiClient } from "./fixtures";

interface Candidate { id: string; digest: string }
interface Requirement {
  id: string; revision: number; state: string; issueId: string | null;
  chatSessionId: string | null; latestCandidate: Candidate;
}
interface ClaimedTask {
  id: string; auth_token: string; mode: string; requirement_id: string;
  is_leader_task: boolean; runtime_id: string; dispatched_at: string;
  trigger_comment_id: string | null; agent: { id: string; instructions: string };
}

// Only the runtime rows are fixtures. Claims, task-token auth, callbacks and
// human decisions use the running API; no installed agent CLI is invoked.
test("external candidate, human approval, squad dispatch and human acceptance", async ({ page }, testInfo) => {
  test.setTimeout(180_000);
  const api = new TestApiClient();
  const apiBase = process.env.NEXT_PUBLIC_API_URL!;
  const runID = randomUUID().slice(0, 8);
  const database = new pg.Client(process.env.DATABASE_URL);
  await database.connect();
  let personalTokenID: string | undefined;
  try {
    const login = await api.login(`squad-delegation-${runID}@multica.test`, "Squad delegation reviewer");
    const workspace = await api.createWorkspace("Squad delegation verification", `squad-delegation-${runID}`);
    await api.markUserOnboarded();
    const request = async (path: string, method = "GET", body?: unknown, token = api.getToken()!, key?: string) => fetch(`${apiBase}${path}`, {
      method,
      headers: {
        Authorization: `Bearer ${token}`, "X-Workspace-ID": workspace.id, "X-Client-Capabilities": "requirement-candidate-provenance-v1", "Content-Type": "application/json",
        ...(key && { "Idempotency-Key": key }),
      },
      ...(body !== undefined && { body: JSON.stringify(body) }),
    });
    const json = async <T,>(path: string, method = "GET", body?: unknown, token?: string, key?: string): Promise<T> => {
      const response = await request(path, method, body, token, key);
      expect(response.ok, `${method} ${path}: ${await response.clone().text()}`).toBe(true);
      return response.json();
    };
    const pat = await json<{ id: string; token: string }>("/api/tokens", "POST", { name: "OpenCode delegation verification", expires_in_days: 1 });
    personalTokenID = pat.id;
    expect(pat.token.startsWith("mul_")).toBe(true);
    const runtimeIDs: string[] = [];
    for (const role of ["leader", "worker"]) {
      const result = await database.query<{ id: string }>(
        `INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
         VALUES ($1, $2, 'cloud', 'e2e_requirement_squad', 'online', 'HTTP callback fixture', '{}'::jsonb, $3, now()) RETURNING id`,
        [workspace.id, `Squad ${role} fixture`, login.user.id],
      );
      runtimeIDs.push(result.rows[0]!.id);
    }
    const leader = await json<{ id: string }>("/api/agents", "POST", { name: "Squad review leader", runtime_id: runtimeIDs[0], permission_mode: "private" });
    const worker = await json<{ id: string }>("/api/agents", "POST", { name: "Squad implementation worker", runtime_id: runtimeIDs[1], permission_mode: "private" });
    const squad = await json<{ id: string }>("/api/squads", "POST", { name: "Approved delivery squad", leader_id: leader.id });
    await json(`/api/squads/${squad.id}/members`, "POST", { member_type: "agent", member_id: worker.id, role: "Implementation" });
    const input = {
      externalRef: `opencode-${runID}`, squadId: squad.id,
      candidate: { goal: "Verify squad delivery", nonGoals: ["No execution before approval"], scope: ["Deliver a verified result"], constraints: ["Use the approved squad"], acceptanceCriteria: ["Human reviews the delivery"], projectId: null },
    };
    const detail = await json<Requirement>(`/api/workspaces/${workspace.id}/requirement-delegations`, "POST", input, pat.token, `external-${runID}`);
    expect(detail.state).toBe("awaiting_approval");
    expect(detail.issueId).toBeNull();
    expect(detail.chatSessionId).toBeNull();
    const beforeApproval = await database.query<{ count: string }>("SELECT count(*) FROM agent_task_queue WHERE requirement_id = $1", [detail.id]);
    expect(beforeApproval.rows[0]!.count).toBe("0");
    const requirementPath = `/api/workspaces/${workspace.id}/requirements/${detail.id}`;

    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token!);
      localStorage.setItem("multica:chat:isOpen", "false");
    }, api.getToken());
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(`/${workspace.slug}/chat?requirement=${detail.id}`, { waitUntil: "domcontentloaded" });
    await page.getByRole("button", { name: "Review and approve", exact: true }).click({ timeout: 60_000 });
    await expect(page.getByRole("alertdialog")).toContainText("Approved delivery squad");
    await expect(page.getByRole("alertdialog")).toContainText("Squad leader");
    await page.getByRole("button", { name: "Create Issue and start squad leader", exact: true }).click();
    await expect.poll(async () => (await json<Requirement>(requirementPath)).state).toBe("executing");
    const approved = await json<Requirement>(requirementPath);
    const issuePath = `/api/issues/${approved.issueId}`;

    const claim = async (runtimeID: string, agentID: string, isLeader: boolean) => {
      const { task } = await json<{ task: ClaimedTask }>(`/api/daemon/runtimes/${runtimeID}/tasks/claim`, "POST", {}, pat.token);
      expect(task?.agent.id).toBe(agentID);
      expect(task.requirement_id).toBe(detail.id);
      expect(task.mode).toBe("execute");
      expect(task.is_leader_task ?? false).toBe(isLeader);
      if (isLeader) expect(task.agent.instructions).toContain("Squad Operating Protocol");
      await json(`/api/daemon/tasks/${task.id}/start`, "POST", { runtime_id: runtimeID, dispatched_at: task.dispatched_at }, pat.token);
      expect((await json<Requirement>(requirementPath, "GET", undefined, task.auth_token)).id).toBe(detail.id);
      return task;
    };
    const comment = (task: ClaimedTask, content: string) => json<{ id: string }>(`${issuePath}/comments`, "POST", {
      content, ...(task.trigger_comment_id && { parent_id: task.trigger_comment_id }),
    }, task.auth_token);
    const complete = (task: ClaimedTask) => json(`/api/daemon/tasks/${task.id}/complete`, "POST", { output: "Verified squad delivery" }, pat.token);

    const firstLeader = await claim(runtimeIDs[0]!, leader.id, true);
    expect((await request(`${requirementPath}/approve`, "POST", { expectedRevision: detail.revision, candidateId: detail.latestCandidate.id, candidateDigest: detail.latestCandidate.digest }, firstLeader.auth_token, `task-token-approve-forbidden-${runID}`)).status).toBe(403);
    await comment(firstLeader, `[@Worker](mention://agent/${worker.id}) Deliver the approved scope and report your verification here.`);
    await complete(firstLeader);
    expect((await json<Requirement>(requirementPath)).state).toBe("executing");
    const work = await claim(runtimeIDs[1]!, worker.id, false);
    expect((await request(issuePath, "PUT", { status: "in_review" }, work.auth_token)).status).toBe(409);
    await comment(work, "Implementation and regression checks complete. Ready for the squad leader's review.");
    await complete(work);
    expect((await json<Requirement>(requirementPath)).state).toBe("executing");
    const finalLeader = await claim(runtimeIDs[0]!, leader.id, true);
    await comment(finalLeader, "Reviewed every acceptance criterion and the worker's verification receipt. Ready for human acceptance.");
    await json(issuePath, "PUT", { status: "in_review" }, finalLeader.auth_token);
    await complete(finalLeader);
    expect((await json<Requirement>(requirementPath)).state).toBe("in_review");

    await page.goto(`/${workspace.slug}/issues/${approved.issueId}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByTestId("requirement-flow-panel")).toBeVisible({ timeout: 45_000 });
    await expect(page.getByRole("button", { name: "Mark as done", exact: true })).toHaveCount(0);
    await page.getByRole("button", { name: "Accept", exact: true }).click();
    await expect(page.getByRole("alertdialog")).not.toContainText("Yixiezuo");
    await expect(page.getByRole("alertdialog").getByRole("combobox")).toHaveCount(0);
    await page.getByLabel("Delivery summary and links").fill("Squad implementation, review, and verification receipts accepted.");
    await page.screenshot({ path: testInfo.outputPath("native-acceptance.png") });
    await page.getByRole("button", { name: "Accept delivery", exact: true }).click();
    await expect.poll(async () => (await json<Requirement>(requirementPath)).state).toBe("done");
    const receipts = await database.query<{ accepted: string; writebacks: string; issues: string }>(
      `SELECT (SELECT count(*) FROM requirement_event WHERE requirement_id = $1 AND action = 'accepted') AS accepted,
              (SELECT count(*) FROM yixiezuo_operation WHERE requirement_id = $1) AS writebacks,
              (SELECT count(*) FROM issue WHERE origin_type = 'requirement_delegation' AND origin_id = $1) AS issues`, [detail.id],
    );
    expect(receipts.rows).toEqual([{ accepted: "1", writebacks: "0", issues: "1" }]);
    await writeFile(testInfo.outputPath("squad-receipt.json"), JSON.stringify({
      verifiedAt: new Date().toISOString(), workspaceId: workspace.id,
      requirementId: detail.id, issueId: approved.issueId,
      execution: "Test runtimes with real HTTP claim, task-token auth and callbacks; no agent CLI",
      state: "done", ...receipts.rows[0],
    }, null, 2));
  } finally {
    if (personalTokenID) {
      await fetch(`${apiBase}/api/tokens/${personalTokenID}`, { method: "DELETE", headers: { Authorization: `Bearer ${api.getToken()}` } });
    }
    await api.deleteWorkspace();
    await database.end();
  }
});
