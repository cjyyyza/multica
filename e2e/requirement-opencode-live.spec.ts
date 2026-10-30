import "./env";
import { test, expect } from "@playwright/test";
import { createHash, randomUUID } from "node:crypto";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { createWriteStream } from "node:fs";
import { copyFile, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import pg from "pg";
import { TestApiClient } from "./fixtures";

const enabled = process.env.MULTICA_RUN_REAL_AGENT_SMOKE === "1" && process.env.MULTICA_OPENCODE_REQUIREMENT_LIVE === "1";
const exec = promisify(execFile);

for (const scenario of ["agent", "squad"] as const) {
test(`real OpenCode uses the official CLI for ${scenario} Plan, execution and human acceptance`, async ({ page, context }, info) => {
  test.skip(!enabled, "explicit real OpenCode and Multica Agent account access only");
  test.setTimeout(1_920_000);
  const cli = process.env.MULTICA_LIVE_CLI_PATH;
  const openCode = process.env.MULTICA_OPENCODE_PATH;
  const model = process.env.MULTICA_OPENCODE_MODEL;
  const selectedTarget = process.env.MULTICA_OPENCODE_LIVE_TARGET ?? "all";
  expect(["all", "agent", "squad"]).toContain(selectedTarget);
  test.skip(selectedTarget !== "all" && selectedTarget !== scenario, "a different target was selected");
  expect(cli && openCode && model, "Provide explicit Multica CLI, OpenCode executable and model").toBeTruthy();
  const base = process.env.NEXT_PUBLIC_API_URL!;
  expect(new URL(base).hostname).toMatch(/^(localhost|127\.0\.0\.1)$/);
  const repo = process.cwd();
  const nonce = randomUUID().slice(0, 8);
  const evidence = info.outputPath("live");
  await mkdir(evidence, { recursive: true });
  const sandbox = path.join(os.tmpdir(), `multica-opencode-live-${nonce}`);
  const controller = path.join(sandbox, "controller");
  await mkdir(path.join(controller, ".opencode", "agents"), { recursive: true });
  const memberCLI = process.platform === "win32" ? "multica.exe" : "multica";
  await copyFile(cli!, path.join(controller, memberCLI));
  await exec(cli!, ["requirement", "create", "--help"], { timeout: 30_000, windowsHide: true });
  const openCodeVersion = (await exec(openCode!, ["--version"], { timeout: 30_000, windowsHide: true })).stdout.trim();
  expect(openCodeVersion, "Read the OpenCode contract version from the selected executable").toMatch(/\d+\.\d+\.\d+/);
  await copyFile(path.join(repo, "examples/opencode/agents/multica.md"), path.join(controller, ".opencode/agents/multica.md"));
  const profile = `native-opencode-${nonce}`;
  const profileDir = path.join(os.homedir(), ".multica", "profiles", profile);
  const api = new TestApiClient();
  const database = new pg.Pool({ connectionString: process.env.DATABASE_URL, max: 2, idleTimeoutMillis: 10_000 });
  database.on("error", error => { void writeFile(path.join(evidence, "database-error.txt"), error.message); });
  await database.query("SELECT 1");
  let daemon: ReturnType<typeof spawn> | undefined;
  let pat: { id: string; token: string } | undefined;
  const daemonLog = createWriteStream(path.join(evidence, "daemon.log"));
  try {
    const login = await api.login(`native-opencode-${nonce}@multica.test`, "Native OpenCode acceptance");
    const workspace = await api.createWorkspace("Native OpenCode acceptance", `native-opencode-${nonce}`);
    await api.markUserOnboarded();
    const call = async (route: string, body?: unknown, key?: string, token = api.getToken()!) => {
      const response = await fetch(`${base}${route}`, { method: body === undefined ? "GET" : "POST", headers: {
        Authorization: `Bearer ${token}`, "X-Workspace-ID": workspace.id, "Content-Type": "application/json", ...(key && { "Idempotency-Key": key }),
      }, ...(body !== undefined && { body: JSON.stringify(body) }) });
      const value = await response.json();
      expect(response.ok, `${route}: ${JSON.stringify(value)}`).toBe(true);
      return value;
    };
    pat = await call("/api/tokens", { name: `OpenCode native acceptance ${nonce}`, expires_in_days: 1 });
    await mkdir(profileDir, { recursive: true });
    await writeFile(path.join(profileDir, "config.json"), JSON.stringify({
      server_url: base, app_url: process.env.PLAYWRIGHT_BASE_URL, token: pat!.token, workspace_id: workspace.id,
      device_name: profile, runtime_name: profile, workspaces_root: path.join(evidence, "workspaces"),
      max_concurrent_tasks: 2, poll_interval: "1s", heartbeat_interval: "5s", agent_timeout: "5m", disable_auto_update: true,
    }), { mode: 0o600 });
    daemon = spawn(cli!, ["--profile", profile, "daemon", "start", "--foreground", "--no-auto-update", "--no-auto-reload"], { cwd: repo, env: process.env, windowsHide: true, stdio: ["ignore", "pipe", "pipe"] });
    daemon.stdout!.pipe(daemonLog); daemon.stderr!.pipe(daemonLog);
    let runtime: any;
    await expect.poll(async () => {
      const rows = await call("/api/runtimes");
      runtime = rows.find((row: any) => row.provider === "claude" && row.status === "online");
      return Boolean(runtime?.id);
    }, { timeout: 90_000, intervals: [1000, 2000] }).toBe(true);
    const instructions = "Only work on the approved isolated requirement. Native Plan is read-only; return a complete structured candidate, never edit before approval. During Execute fix only its explicit temporary fixture, run the supplied regression tests, and report the absolute path, test receipt and SHA256. Never change the Multica repository, install packages, commit or contact external systems. If assigned as Squad leader, delegate implementation by an issue comment mentioning the listed worker, then stop your turn. After the worker replies, independently verify its tests and use multica issue status <issue-id> in_review. Do not implement the worker's work yourself or mark done; humans accept in Multica.";
    const single = await call("/api/agents", { name: `Native single ${nonce}`, runtime_id: runtime.id, permission_mode: "private", instructions, max_concurrent_tasks: 1 });
    const leader = await call("/api/agents", { name: `Native leader ${nonce}`, runtime_id: runtime.id, permission_mode: "private", instructions, max_concurrent_tasks: 1 });
    const worker = await call("/api/agents", { name: `Native worker ${nonce}`, runtime_id: runtime.id, permission_mode: "private", instructions: "You implement the approved Squad leader's delegated bug fix. Edit only the explicit temporary fixture, preserve its regression tests, run them, compute SHA256, and reply with evidence to the leader in the task's Issue thread. Never approve or accept a requirement, mark it done, install packages, commit, or change any other repository.", max_concurrent_tasks: 1 });
    const squad = await call("/api/squads", { name: `Native delivery squad ${nonce}`, leader_id: leader.id });
    await call(`/api/squads/${squad.id}/members`, { member_type: "agent", member_id: worker.id, role: "Implement and test the approved bug fix" });
    await context.addCookies([{ name: "multica-locale", value: "en", url: process.env.PLAYWRIGHT_BASE_URL! }]);
    await page.addInitScript(token => { localStorage.setItem("multica_token", token); localStorage.setItem("multica:chat:isOpen", "false"); }, api.getToken()!);
    await page.setViewportSize({ width: 1440, height: 1000 });
    const receipts = [];
    for (const target of [{ kind: "agent", id: single.id }, { kind: "squad", id: squad.id }].filter(target => target.kind === scenario)) {
      const dir = path.join(evidence, target.kind);
      await mkdir(dir, { recursive: true });
      const fixture = path.join(sandbox, target.kind);
      await mkdir(fixture, { recursive: true });
      const module = path.join(fixture, "discount.mjs");
      const regression = path.join(fixture, "discount.test.mjs");
      await writeFile(module, "export function discount(amount, percent) { return Math.round(amount * (1 + percent / 100) * 100) / 100; }\n");
      await writeFile(regression, 'import { test } from "node:test"; import assert from "node:assert/strict"; import { discount } from "./discount.mjs";\ntest("percentage discount decreases price", () => assert.equal(discount(100, 10), 90));\ntest("zero and full discounts", () => { assert.equal(discount(32, 0), 32); assert.equal(discount(32, 100), 0); });\n');
      const testHash = createHash("sha256").update(await readFile(regression)).digest("hex");
      const before = createHash("sha256").update(await readFile(module)).digest("hex");
      await expect(exec(process.execPath, ["--test", regression])).rejects.toMatchObject({ code: 1 });
      const source = `Fix the discount calculation in ${module}. It currently increases a price instead of subtracting a percentage. Keep regression tests ${regression} unchanged. Acceptance: run "${process.execPath}" --test "${regression}" successfully, covering 10%, 0% and 100% discount. Change only discount.mjs, report its absolute path, tests and SHA256. Do not install packages or commit. This is an isolated real-agent acceptance with a complete requirement. No clarification is needed. Project is null. In native Plan only inspect and propose the candidate; do not change files until the human approves in Multica. ${target.kind === "squad" ? `The squad leader must delegate implementation to the existing worker ${worker.id}, wait for its reply and verify before submitting in_review.` : "The selected single Agent implements and verifies after approval."}`;
      const ideaFile = path.join(controller, `idea-${target.kind}.md`);
      await writeFile(ideaFile, source);
      const externalRef = `native-opencode-${nonce}-${target.kind}`;
      const control = async (phase: string, prompt: string) => {
        const promptFile = path.join(controller, `prompt-${target.kind}-${phase}.md`);
        await writeFile(promptFile, prompt);
        await exec("go", ["-C", path.join(repo, "server"), "test", "-tags=agentintegration", "./pkg/agent", "-run", "^TestOpenCodeMulticaControllerRealSmoke$", "-count=1", "-v", "-timeout=270s"], {
          cwd: repo, timeout: 290_000, maxBuffer: 2_000_000, windowsHide: true,
          env: { ...process.env, MULTICA_RUN_REAL_AGENT_SMOKE: "1", MULTICA_OPENCODE_CWD: controller, MULTICA_OPENCODE_PROMPT: promptFile, MULTICA_OPENCODE_EVIDENCE: path.join(dir, `opencode-${phase}.json`), MULTICA_OPENCODE_VERSION: openCodeVersion, MULTICA_SERVER_URL: base, MULTICA_WORKSPACE_ID: workspace.id, MULTICA_TOKEN: pat!.token },
        }).catch(async error => { await writeFile(path.join(dir, `controller-${phase}-error.log`), `${error.stdout ?? ""}\n${error.stderr ?? ""}`); throw new Error(`OpenCode ${target.kind}/${phase} failed; see sanitized controller evidence`); });
      };
      await control("plan", `Coordinate through Multica only. Execute exactly one shell command now: ./${memberCLI} requirement --strict create --${target.kind} ${target.id} --external-ref ${externalRef} --idea-file ${path.basename(ideaFile)} --idempotency-key ${externalRef}-plan . This is an asynchronous planning request; return its requirement ID and current state immediately. Do not wait, poll, discover targets, inspect CLI code or retrieve a candidate in this stage. The test host owns subsequent polling. The human review URL is ${process.env.PLAYWRIGHT_BASE_URL}/${workspace.slug}/chat?requirement=<returned-id>. Do not implement or approve anything; never read profiles or expose credentials.`);
      const found = await call(`/api/workspaces/${workspace.id}/requirements?externalRef=${externalRef}`, undefined, undefined, pat!.token);
      expect(found.requirements).toHaveLength(1);
      const id = found.requirements[0].id;
      const route = `/api/workspaces/${workspace.id}/requirements/${id}`;
      const tasks = async () => (await database.query("SELECT id::text, agent_id::text, mode, status, is_leader_task, delegated_from_task_id::text, result FROM agent_task_queue WHERE requirement_id=$1 ORDER BY created_at", [id])).rows;
      const waitFor = async (state: string, timeout: number) => {
        await expect.poll(async () => {
          const detail = await call(route);
          const runs = await tasks();
          const failed = runs.find((run: any) => ["failed", "blocked", "cancelled"].includes(run.status));
          if (failed) { await writeFile(path.join(dir, "failure.json"), JSON.stringify({ detail, runs }, null, 2)); throw new Error(`Real ${target.kind} run ${failed.status}: ${JSON.stringify(failed.result)}`); }
          return detail.state;
        }, { timeout, intervals: [1000, 2000] }).toBe(state);
      };
      await waitFor("awaiting_approval", 270_000);
      const planned = await call(route);
      expect(planned.issueId).toBeNull();
      expect(planned.latestCandidate.createdByTaskId).toBeTruthy();
      expect((await tasks()).every((run: any) => run.mode === "plan")).toBe(true);
      expect(createHash("sha256").update(await readFile(module)).digest("hex")).toBe(before);
      await writeFile(path.join(dir, "plan.json"), JSON.stringify(planned, null, 2));
      await page.goto(`/${workspace.slug}/chat?session=${planned.chatSessionId}&requirement=${id}`, { waitUntil: "domcontentloaded" });
      await page.getByRole("button", { name: "Review and approve", exact: true }).first().click({ timeout: 60_000 });
      await page.screenshot({ path: path.join(dir, "approval.png") });
      await page.getByRole("alertdialog").getByRole("button", { name: target.kind === "squad" ? "Create Issue and start squad leader" : "Create Issue and start agent", exact: true }).click();
      await waitFor("in_review", 480_000);
      // The leader submits review while its own turn is still running. Wait
      // for its final callback before recording runs and accepting delivery.
      await expect.poll(async () => (await tasks()).every((run: any) => run.status === "completed"), { timeout: 90_000, intervals: [1000, 2000] }).toBe(true);
      const reviewed = await call(route);
      const runs = await tasks();
      expect(runs.filter((run: any) => run.mode === "execute").length).toBeGreaterThan(0);
      if (target.kind === "squad") expect(runs.some((run: any) => run.agent_id === worker.id && run.mode === "execute")).toBe(true);
      else expect(runs.filter((run: any) => run.mode === "execute").every((run: any) => run.agent_id === single.id)).toBe(true);
      const verification = await exec(process.execPath, ["--test", regression]);
      const after = createHash("sha256").update(await readFile(module)).digest("hex");
      expect(after).not.toBe(before);
      expect(createHash("sha256").update(await readFile(regression)).digest("hex")).toBe(testHash);
      await writeFile(path.join(dir, "verification.txt"), verification.stdout);
      await control("result", `Read the real Multica delivery for requirement ${id} using ./${memberCLI} requirement --strict result ${id} --output json. Invoke the official CLI, inspect the Issue, runs and comments, and report the actual implementation and regression-test results, artifact path, SHA256 and current in_review state. Do not approve or accept; the human is reviewing in Multica. Do not implement anything or expose credentials.`);
      await page.goto(`/${workspace.slug}/issues/${reviewed.issueId}`, { waitUntil: "domcontentloaded" });
      await page.getByRole("button", { name: "Accept", exact: true }).first().click({ timeout: 60_000 });
      await expect(page.getByRole("alertdialog")).not.toContainText("Yixiezuo");
      await page.getByLabel("Delivery summary and links").fill(`Verified real OpenCode → ${target.kind} Plan → human approval → implementation and regression tests. SHA256 ${after}`);
      await page.screenshot({ path: path.join(dir, "acceptance.png") });
      await page.getByRole("button", { name: "Accept delivery", exact: true }).click();
      await expect.poll(async () => (await call(route)).state).toBe("done");
      const counts = (await database.query("SELECT (SELECT count(*) FROM requirement_event WHERE requirement_id=$1 AND action='approved') AS approved, (SELECT count(*) FROM requirement_event WHERE requirement_id=$1 AND action='accepted') AS accepted, (SELECT count(*) FROM yixiezuo_operation WHERE requirement_id=$1) AS writebacks", [id])).rows[0];
      expect(counts).toEqual({ approved: "1", accepted: "1", writebacks: "0" });
      receipts.push({ target: target.kind, requirementId: id, issueId: reviewed.issueId, state: "done", before, after, testHash, runs, ...counts });
      await writeFile(path.join(evidence, "receipt.json"), JSON.stringify({ verifiedAt: new Date().toISOString(), workspaceId: workspace.id, model, openCode, multicaRuntime: "real installed Claude, native read-only Plan", receipts }, null, 2));
    }
  } finally {
    if (daemon) {
      await exec(cli!, ["--profile", profile, "daemon", "stop"], { timeout: 30_000, windowsHide: true }).catch(() => { daemon?.kill(); });
    }
    daemonLog.end();
    await copyFile(path.join(profileDir, "daemon.log"), path.join(evidence, "daemon-profile.log")).catch(() => {});
    try {
      if (pat) await fetch(`${base}/api/tokens/${pat.id}`, { method: "DELETE", headers: { Authorization: `Bearer ${api.getToken()}` } });
      await api.deleteWorkspace();
    } finally {
      await database.end();
      // Remove only the private, UUID-named profile created above.
      if (path.dirname(profileDir) === path.join(os.homedir(), ".multica", "profiles") && path.basename(profileDir) === profile) await rm(profileDir, { recursive: true, force: true });
    }
  }
});
}
