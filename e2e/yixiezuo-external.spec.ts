import "./env";

import { execFile } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { promisify } from "node:util";
import { expect, test } from "@playwright/test";
import { TestApiClient } from "./fixtures";

const execFileAsync = promisify(execFile);

function verifyPublication(before: any, after: any, summary: string) {
  expect(after.source).toEqual(before.source);
  for (const field of ["title", "description", "status"] as const) {
    expect(after[field], `source ${field} must remain unchanged`).toEqual(before[field]);
  }
  const comments = after.comments.filter((comment: { content: string }) => comment.content.includes(summary));
  expect(comments).toHaveLength(1);
  return comments[0];
}

test("explicitly confirmed source comment is published and read back", async ({}, testInfo) => {
  test.skip(process.env.MULTICA_RUN_YIXIEZUO_ACCEPTANCE !== "1", "requires explicit external-write opt-in");
  test.setTimeout(300_000);
  const sourceURL = process.env.MULTICA_ACCEPTANCE_SOURCE_URL;
  const binary = process.env.MULTICA_ACCEPTANCE_CLI;
  const popoCLI = process.env.MULTICA_POPO_CLI;
  const baseURL = process.env.NEXT_PUBLIC_API_URL;
  expect(sourceURL, "select a writable test issue explicitly").toBeTruthy();
  expect(binary, "build the current CLI before acceptance").toBeTruthy();
  expect(popoCLI, "select the authenticated POPO CLI explicitly").toBeTruthy();
  expect(baseURL, "select the local acceptance API explicitly").toMatch(/^http:\/\/(localhost|127\.0\.0\.1):\d+$/);

  const evidence = process.env.MULTICA_ACCEPTANCE_EVIDENCE_DIR || testInfo.outputPath("external-evidence");
  await mkdir(evidence, { recursive: true });
  const save = (name: string, value: unknown) => writeFile(path.join(evidence, name), JSON.stringify(value, null, 2));
  const api = new TestApiClient();
  const nonce = randomUUID();
  await api.login(`e2e-source-${nonce}@test.local`, "Source acceptance");
  const workspace = await api.ensureWorkspace("Source acceptance", `source-${nonce}`);
  const cliEnv = {
    ...process.env,
    MULTICA_TOKEN: api.getToken()!,
    MULTICA_WORKSPACE_ID: workspace.id,
    MULTICA_TASK_CONFIG_ROOT: path.join(evidence, "cli-profile"),
  };
  const runCLI = async (name: string, args: string[]) => {
    const result = await execFileAsync(binary!, ["--server-url", baseURL!, "--workspace-id", workspace.id,
      "--profile", `acceptance-${nonce}`, "yixiezuo", ...args, "--cli", popoCLI!],
    { env: cliEnv, timeout: 150_000, maxBuffer: 4 * 1024 * 1024 });
    await writeFile(path.join(evidence, `${name}.log`), result.stdout + result.stderr);
    return result.stdout.trim();
  };
  const request = async (suffix: string, method = "GET", body?: unknown) => {
    const response = await fetch(`${baseURL}/api/workspaces/${workspace.id}/yixiezuo${suffix}`, {
      method,
      headers: { Authorization: `Bearer ${api.getToken()}`, "X-Workspace-ID": workspace.id, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const result = await response.json();
    expect(response.ok, JSON.stringify(result)).toBe(true);
    return result;
  };
  try {
    if (process.env.MULTICA_ACCEPTANCE_READBACK_ONLY === "1") {
      const before = JSON.parse(await readFile(path.join(evidence, "pm-live-before.json"), "utf8"));
      const completed = JSON.parse(await readFile(path.join(evidence, "pm-live-completed.json"), "utf8"));
      expect(completed.state).toBe("succeeded");
      expect(new URL(sourceURL!).searchParams.get("issue_id") || new URL(sourceURL!).pathname.split("/").pop()).toBe(before.snapshot.source.id);
      const after = JSON.parse(await runCLI("pm-live-recovery-readback", ["preview", sourceURL!]));
      const comment = verifyPublication(before.snapshot, after, completed.payload.summary);
      await save("pm-live-receipt.json", { source: after.source, operation_id: completed.id, comment, state: completed.state, verification: "readback_only" });
      return;
    }
    const imported = JSON.parse(await runCLI("pm-live-import", ["import", sourceURL!, "--confirm"]));
    expect(imported.issue.id).toBeTruthy();
    const issueId = imported.issue.id;
    const before = (await request(`/imports/${issueId}`)).import;
    await save("pm-live-before.json", before);
    const summary = `【Multica链路验收 ${nonce}】仅验证确认后的评论回写与读回，不代表本业务任务完成；不修改标题、描述或状态。`;
    const queued = await request(`/imports/${issueId}/publish`, "POST", {
      summary, confirmed: true, source_digest: before.snapshot.digest, revision: before.revision,
    });
    await save("pm-live-queued.json", queued);
    expect(queued.state).toBe("pending");
    await runCLI("pm-live-publish", ["bridge", "--once"]);
    const completed = await request(`/operations/${queued.id}`);
    await save("pm-live-completed.json", completed);
    expect(completed.state, JSON.stringify(completed)).toBe("succeeded");

    await runCLI("pm-live-idle-bridge", ["bridge", "--once"]);
    const after = JSON.parse(await runCLI("pm-live-readback", ["preview", sourceURL!]));
    await save("pm-live-after.json", after);
    const comment = verifyPublication(before.snapshot, after, summary);
    await save("pm-live-receipt.json", { source: after.source, operation_id: queued.id, comment, state: completed.state });
    expect((await request(`/imports/${issueId}`)).import.state).toBe("published");
  } finally {
    await api.deleteWorkspace();
  }
});
