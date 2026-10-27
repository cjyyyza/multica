import "./env";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { expect, test, _electron as electron } from "@playwright/test";

test.beforeEach(async () => {
  if (process.env.MULTICA_RUN_SEVEN_ACCEPTANCE !== "1") return;
  const evidence = process.env.MULTICA_SEVEN_EVIDENCE_DIR!;
  const session = JSON.parse(await readFile(path.join(evidence, "session.json"), "utf8"));
  expect(session.baseURL).toBe("http://127.0.0.1:18980");
  const health = await (await fetch(`${session.baseURL}/health`)).json();
  expect(health.commit).toBe("4d32633b9-seven-9dddbb2a");
});

test("native desktop isolated login and Requirement surface", async () => {
  test.skip(process.env.MULTICA_RUN_SEVEN_ACCEPTANCE !== "1", "explicit native desktop acceptance only");
  test.setTimeout(180_000);
  const evidence = process.env.MULTICA_SEVEN_EVIDENCE_DIR!;
  const session = JSON.parse(await readFile(path.join(evidence, "session.json"), "utf8"));
  const state = JSON.parse(await readFile(path.join(evidence, "state.json"), "utf8"));
  const home = path.join(evidence, "desktop-home");
  const roaming = path.join(home, "AppData", "Roaming");
  await mkdir(path.join(home, ".multica"), { recursive: true });
  await mkdir(roaming, { recursive: true });
  await writeFile(path.join(home, ".multica", "desktop_prefs.json"), JSON.stringify({ autoStart: false, autoStop: false }));
  const app = await electron.launch({
    executablePath: path.resolve("node_modules/electron/dist/electron.exe"),
    args: [path.resolve("apps/desktop/out/main/index.js"), "--lang=en-US"],
    env: { ...process.env, HOME: home, USERPROFILE: home, APPDATA: roaming,
      LOCALAPPDATA: path.join(home, "AppData", "Local"), DESKTOP_APP_SUFFIX: "seven-9dddbb2a",
      MULTICA_TASK_CONFIG_ROOT: path.join(home, "task-config"), ELECTRON_RENDERER_URL: "" },
  });
  try {
    const actualHome = await app.evaluate(() => process.getBuiltinModule("node:os").homedir());
    expect(path.resolve(actualHome)).toBe(path.resolve(home));
    const page = await app.firstWindow();
    await app.evaluate(({ BrowserWindow }) => { BrowserWindow.getAllWindows()[0]?.show(); BrowserWindow.getAllWindows()[0]?.focus(); });
    await page.waitForLoadState("domcontentloaded");
    const screenshot = async (name: string) => {
      await page.screenshot({ path: path.join(evidence, name), timeout: 5_000 }).catch(async (error) => {
        await writeFile(path.join(evidence, `${name}.error.txt`), String(error));
      });
    };
    await page.waitForFunction(() => typeof window.daemonAPI?.getPrefs === "function");
    expect((await page.evaluate(() => window.daemonAPI.getPrefs())).autoStart).toBe(false);
    await writeFile(path.join(evidence, "desktop-before-login.txt"), await page.locator("body").innerText());
    await app.evaluate(({ app }, token) => {
      app.emit("open-url", { preventDefault() {} }, `multica://auth/callback?token=${token}`);
    }, session.token);
    await expect(page.getByText("Seven-case acceptance", { exact: true }).first()).toBeVisible({ timeout: 45_000 });
    await screenshot("desktop-authenticated.png");
    await writeFile(path.join(evidence, "desktop-authenticated.txt"), await page.locator("body").innerText());
    expect((await page.evaluate(() => window.daemonAPI.getPrefs())).autoStart).toBe(false);
    await writeFile(path.join(evidence, "desktop-nav.html"), await page.locator("[data-sidebar]").first().evaluate(el => el.outerHTML));
    await page.locator('[data-sidebar="trigger"]').click();
    await page.getByText("聊天", { exact: true }).filter({ visible: true }).first().click();
    await page.getByRole("button", { name: "新建需求", exact: true }).first().click();
    const intake = page.getByRole("dialog");
    await intake.getByLabel("来源 URL").fill("https://dj01.pm.netease.com/issues/108402");
    await intake.getByRole("button", { name: "预览来源", exact: true }).click();
    await expect(intake.getByRole("button", { name: "创建需求", exact: true })).toBeEnabled({ timeout: 90_000 });
    await expect(intake.getByText("multica-acceptance-9dddbb2a.txt", { exact: true })).toBeVisible();
    await screenshot("desktop-source-preview.png");
    await intake.getByRole("button", { name: "创建需求", exact: true }).click();
    const panel = page.getByTestId("requirement-flow-panel").filter({ visible: true }).first();
    if (!state.issueId) {
      await expect(panel.getByText("待批准", { exact: true })).toBeVisible({ timeout: 45_000 });
      await panel.getByRole("button", { name: "审查并批准", exact: true }).click();
      const review = page.getByRole("alertdialog");
      await expect(review.getByText("multica-acceptance-9dddbb2a.txt", { exact: true })).toBeVisible();
      await expect(review.getByText("【链路验收】仅在临时目录生成验收产物，不代表108402业务完成", { exact: true })).toBeVisible();
      await screenshot("desktop-candidate-review.png");
      await review.getByRole("button", { name: "创建任务并启动智能体", exact: true }).click();
    }
    await expect(panel.getByText("打开任务", { exact: true })).toBeVisible({ timeout: 30_000 });
    await expect(panel.getByText("此需求 Chat 已只读。", { exact: true })).toBeVisible();
    const tabs = async () => page.evaluate(() => JSON.parse(localStorage.getItem("multica_tabs") || "null"));
    const beforeIssue = await tabs();
    const requirementTabs = beforeIssue.state.byWorkspace[session.workspace.slug].tabs.filter((tab: { url: string }) => tab.url.includes(`requirement=${state.requirementId}`));
    expect(requirementTabs).toHaveLength(1);
    await panel.getByText("打开任务", { exact: true }).click();
    await expect(page.getByTestId("requirement-flow-panel").filter({ visible: true }).getByText("已完成", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
    const openedTabs = await tabs();
    const count = openedTabs.state.byWorkspace[session.workspace.slug].tabs.length;
    await page.locator(`[data-tab-id="${requirementTabs[0].id}"]`).click();
    await page.getByTestId("requirement-flow-panel").filter({ visible: true }).getByText("打开任务", { exact: true }).first().click();
    expect((await tabs()).state.byWorkspace[session.workspace.slug].tabs).toHaveLength(count);
    await writeFile(path.join(evidence, "desktop-approved.txt"), await page.locator("body").innerText());
    await screenshot("desktop-approved-issue.png");
    const response = await fetch(`${session.baseURL}/api/workspaces/${session.workspace.id}/requirements/${state.requirementId}`, {
      headers: { Authorization: `Bearer ${session.token}`, "X-Workspace-ID": session.workspace.id },
    });
    expect(response.ok).toBe(true);
    const detail = await response.json();
    expect(detail.issueId).toBeTruthy();
    state.issueId = detail.issueId;
    await writeFile(path.join(evidence, "state.json"), JSON.stringify(state, null, 2));
    await writeFile(path.join(evidence, "desktop-approved-detail.json"), JSON.stringify(detail, null, 2));
    await writeFile(path.join(evidence, "desktop-tabs.json"), await page.evaluate(() => localStorage.getItem("multica_tabs") || "null"));
  } catch (error) {
    await writeFile(path.join(evidence, "desktop-error-reason.txt"), String(error));
    const page = await app.firstWindow();
    await writeFile(path.join(evidence, "desktop-error.txt"), await page.locator("body").innerText());
    await page.screenshot({ path: path.join(evidence, "desktop-error.png"), timeout: 5_000 }).catch(() => {});
    throw error;
  } finally {
    await app.close();
  }
});

test("native desktop reopen, workspace switch and reconnect", async () => {
  test.skip(process.env.MULTICA_RUN_SEVEN_ACCEPTANCE !== "1", "explicit native desktop recovery only");
  test.setTimeout(180_000);
  const evidence = process.env.MULTICA_SEVEN_EVIDENCE_DIR!;
  const session = JSON.parse(await readFile(path.join(evidence, "session.json"), "utf8"));
  const state = JSON.parse(await readFile(path.join(evidence, "state.json"), "utf8"));
  const second = JSON.parse(await readFile(path.join(evidence, "second-workspace.json"), "utf8"));
  const home = path.join(evidence, "desktop-home");
  const app = await electron.launch({
    executablePath: path.resolve("node_modules/electron/dist/electron.exe"),
    args: [path.resolve("apps/desktop/out/main/index.js")],
    env: { ...process.env, HOME: home, USERPROFILE: home, APPDATA: path.join(home, "AppData", "Roaming"),
      LOCALAPPDATA: path.join(home, "AppData", "Local"), DESKTOP_APP_SUFFIX: "seven-9dddbb2a",
      MULTICA_TASK_CONFIG_ROOT: path.join(home, "task-config"), ELECTRON_RENDERER_URL: "" },
  });
  try {
    const page = await app.firstWindow();
    const sockets: { path: string; opened: number }[] = [];
    page.on("websocket", socket => sockets.push({ path: new URL(socket.url()).pathname, opened: Date.now() }));
    await app.evaluate(({ BrowserWindow }) => { BrowserWindow.getAllWindows()[0]?.show(); BrowserWindow.getAllWindows()[0]?.focus(); });
    await expect(page.getByTestId("requirement-flow-panel").first()).toBeVisible({ timeout: 45_000 });
    expect((await page.evaluate(() => window.daemonAPI.getPrefs())).autoStart).toBe(false);
    const storedTabs = async () => page.evaluate(() => JSON.parse(localStorage.getItem("multica_tabs") || "null"));
    const restored = await storedTabs();
    expect(restored.state.activeWorkspaceSlug).toBe(session.workspace.slug);
    const approved = JSON.parse(await readFile(path.join(evidence, "desktop-approved-detail.json"), "utf8"));
    expect(restored.state.byWorkspace[session.workspace.slug].tabs.some((tab: { url: string }) => [state.issueId, approved.issue.identifier].some(id => tab.url.endsWith(`/issues/${id}`)))).toBe(true);
    const openWorkspaceMenu = async (name: string) => {
      const label = page.getByText(name, { exact: true }).first();
      if ((await label.boundingBox())!.x < 0) await page.locator('[data-sidebar="trigger"]').click();
      await label.click();
    };
    await openWorkspaceMenu(session.workspace.name);
    await page.getByRole("menuitem").filter({ hasText: second.name }).click();
    await expect.poll(async () => (await storedTabs()).state.activeWorkspaceSlug).toBe(second.slug);
    await expect(page.getByTestId("requirement-flow-panel")).toHaveCount(0);
    await openWorkspaceMenu(second.name);
    await page.getByRole("menuitem").filter({ hasText: session.workspace.name }).click();
    await expect.poll(async () => (await storedTabs()).state.activeWorkspaceSlug).toBe(session.workspace.slug);
    await expect(page.getByTestId("requirement-flow-panel").first()).toBeVisible();
    const beforeRestart = sockets.length;
    const { promisify } = await import("node:util");
    const { execFile } = await import("node:child_process");
    await promisify(execFile)("wsl.exe", ["-d", "Ubuntu-24.04", "-e", "docker", "restart", "multica-seven-9dddbb2a-api"], { windowsHide: true, timeout: 45_000 });
    await expect.poll(() => sockets.length, { timeout: 45_000 }).toBeGreaterThan(beforeRestart);
    await expect(page.getByTestId("requirement-flow-panel").getByText("已完成", { exact: true }).first()).toBeVisible({ timeout: 30_000 });
    await writeFile(path.join(evidence, "case6-native-recovery.json"), JSON.stringify({
      nativeElectron: true, persistedTabsRestored: true, automaticDaemonStart: false,
      workspaceSwitch: [session.workspace.slug, second.slug, session.workspace.slug], oldRequirementAbsentInSecondWorkspace: true,
      dockerBackendRestarted: true, websocketReconnected: true, sockets,
      finalTabs: (await storedTabs()).state,
    }, null, 2));
    await page.screenshot({ path: path.join(evidence, "desktop-native-recovered.png"), timeout: 5_000 });
  } catch (error) {
    const page = await app.firstWindow();
    await writeFile(path.join(evidence, "desktop-recovery-error.txt"), `${String(error)}\n\n${await page.locator("body").innerText()}`);
    throw error;
  } finally {
    await app.close();
  }
});
