import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { run } from "./multica.mjs";

const env = { MULTICA_URL: "https://multica.example", MULTICA_WORKSPACE_ID: "ws-1", MULTICA_TOKEN: "private-test-token" };

test("native Plan selects either target, retries identical input, and keeps credentials in headers", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "opencode-controller-"));
  try {
    const ideaFile = path.join(dir, "idea.md");
    await writeFile(ideaFile, "Repair session renewal");
    const calls = [];
    const request = async (url, init) => { calls.push({ url, init }); return Response.json({ id: "req-1", state: "clarifying", issueId: null }); };
    for (const target of ["agent", "squad"]) {
      const args = ["plan", `--${target}`, `${target}-1`, "--ref", "source-1", "--key", "stable-plan", "--idea-file", ideaFile];
      await run(args, env, request); await run(args, env, request);
      assert.deepEqual(calls.at(-1), calls.at(-2));
      const { url, init } = calls.at(-1);
      assert.equal(url, `${env.MULTICA_URL}/api/workspaces/ws-1/requirement-requests`);
      assert.equal(init.headers["Idempotency-Key"], "stable-plan");
      assert.equal(init.headers.Authorization, `Bearer ${env.MULTICA_TOKEN}`);
      assert.deepEqual(JSON.parse(init.body), { externalRef: "source-1", idea: "Repair session renewal", [`${target}Id`]: `${target}-1` });
      assert.ok(!init.body.includes(env.MULTICA_TOKEN));
    }
    await run(["reply", "req-1", "--revision", "7", "--key", "stable-reply", "--content-file", ideaFile], env, request);
    assert.deepEqual(JSON.parse(calls.at(-1).init.body), { expectedRevision: 7, content: "Repair session renewal" });
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test("approval gates and invalid targets fail before making a request", async () => {
  let calls = 0;
  const request = async () => { calls++; return Response.json({}); };
  for (const args of [["approve", "req-1"], ["accept", "req-1"], ["plan", "--agent", "a", "--squad", "s", "--ref", "r"], ["reply", "req-1", "--revision", "0", "--key", "k"]]) {
    await assert.rejects(run(args, env, request));
  }
  assert.equal(calls, 0);
});

test("targets includes the workspace slug needed for the human approval URL", async () => {
  const called = [];
  const result = await run(["targets"], env, async url => {
    called.push(new URL(url).pathname);
    return Response.json(url.endsWith("/ws-1") ? { id: "ws-1", slug: "team" } : []);
  });
  assert.equal(result.workspace.slug, "team");
  assert.deepEqual(called.sort(), ["/api/agents", "/api/runtimes", "/api/squads", "/api/workspaces/ws-1"]);
});

test("list recovers a lost creation response and preserves API conflict evidence", async () => {
  let called;
  const request = async url => { called = url; return Response.json({ requirements: [{ id: "req-1" }], hasMore: false }); };
  const found = await run(["list", "--ref", "opencode/run 42"], env, request);
  assert.equal(new URL(called).searchParams.get("externalRef"), "opencode/run 42");
  assert.equal(found.requirements[0].id, "req-1");
  await assert.rejects(run(["get", "req-1"], env, async () => Response.json({ code: "revision_conflict", actual_revision: 8 }, { status: 409 })), /actual_revision.*8/);
});
