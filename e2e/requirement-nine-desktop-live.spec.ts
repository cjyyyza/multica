import "./env";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { expect, test, _electron as electron } from "@playwright/test";
import { TestApiClient } from "./fixtures";

const enabled = process.env.MULTICA_RUN_REQUIREMENT_NINE === "1";
const evidence = process.env.MULTICA_NINE_EVIDENCE_DIR || "";

async function launchDesktop() {
  const home = path.join(evidence, "desktop-nine-home");
  const config = path.join(home, ".multica");
  await mkdir(config, { recursive: true });
  await mkdir(path.join(home, "AppData", "Roaming"), { recursive: true });
  await mkdir(path.join(home, "AppData", "Local"), { recursive: true });
  await writeFile(path.join(config, "desktop_prefs.json"), JSON.stringify({ autoStart: false, autoStop: false }));
  await writeFile(path.join(config, "desktop.json"), JSON.stringify({ schemaVersion: 1,
    apiUrl: "http://127.0.0.1:18208", wsUrl: "ws://127.0.0.1:18208/ws", appUrl: "http://127.0.0.1:13128" }));
  const app = await electron.launch({
    executablePath: path.resolve("node_modules/electron/dist/electron.exe"),
    args: [path.resolve("apps/desktop/out/main/index.js")],
    env: { ...process.env, HOME: home, USERPROFILE: home,
      APPDATA: path.join(home, "AppData", "Roaming"), LOCALAPPDATA: path.join(home, "AppData", "Local"),
      DESKTOP_APP_SUFFIX: "nine-20261027", MULTICA_TASK_CONFIG_ROOT: path.join(home, "task-config"), ELECTRON_RENDERER_URL: "" },
  });
  expect(path.resolve(await app.evaluate(() => process.getBuiltinModule("node:os").homedir()))).toBe(path.resolve(home));
  const page = await app.firstWindow();
  page.setDefaultTimeout(30_000);
  await page.waitForFunction(() => window.desktopAPI?.runtimeConfig.ok);
  expect(await page.evaluate(() => {
    const config = window.desktopAPI.runtimeConfig;
    return config.ok ? config.config.apiUrl : null;
  })).toBe("http://127.0.0.1:18208");
  return { app, page };
}

test("native desktop opens the existing Requirement and reuses its Issue tab", async () => {
  test.skip(!enabled || process.env.MULTICA_NINE_PHASE !== "desktop", "explicit native acceptance only");
  test.setTimeout(180_000);
  const session = JSON.parse(await readFile(path.join(evidence, "session.json"), "utf8"));
  expect(session.baseURL).toBe("http://127.0.0.1:18208");
  const operation = JSON.parse(await readFile(path.join(evidence, "draft-fixed-result.json"), "utf8"));
  const { app, page } = await launchDesktop();
  try {
    await page.waitForFunction(() => typeof window.daemonAPI?.getPrefs === "function");
    expect((await page.evaluate(() => window.daemonAPI.getPrefs())).autoStart).toBe(false);
    await app.evaluate(({ app }, token) => app.emit("open-url", { preventDefault() {} }, `multica://auth/callback?token=${token}`), session.token);
    const secondLabel = page.getByText("Requirement acceptance second", { exact: true }).filter({ visible: true }).first();
    if (await secondLabel.isVisible()) {
      if ((await secondLabel.boundingBox())!.x < 0) await page.locator('[data-sidebar="trigger"]').click();
      await secondLabel.click();
      await page.getByRole("menuitem").filter({ has: page.getByText(session.workspace.name, { exact: true }) }).click();
    }
    const workspaceLabel = page.getByText(session.workspace.name, { exact: true }).first();
    await expect(workspaceLabel).toBeVisible({ timeout: 45_000 });
    if ((await workspaceLabel.boundingBox())!.x < 0) await page.locator('[data-sidebar="trigger"]').click();
    await page.getByText(/^(Chat|聊天)$/).filter({ visible: true }).first().click();
    await page.getByText(operation.payload.draft.title, { exact: true }).filter({ visible: true }).first().click();
    const panel = page.locator('[data-testid="requirement-flow-panel"]:not([inert] *)').filter({ visible: true }).first();
    await expect(panel).toBeVisible();
    await expect(panel.getByText(/^(Open Issue|打开任务)$/)).toBeVisible();
    const tabs = () => page.evaluate(() => JSON.parse(localStorage.getItem("multica_tabs") || "null"));
    const before = (await tabs()).state.byWorkspace[session.workspace.slug].tabs;
    const requirementTab = before.find((tab: { url: string }) => tab.url.includes("/chat"));
    expect(requirementTab).toBeTruthy();
    await panel.getByText(/^(Open Issue|打开任务)$/).click();
    await expect(page.getByTestId("requirement-flow-panel").filter({ visible: true }).first()).toBeVisible();
    const opened = await tabs();
    const count = opened.state.byWorkspace[session.workspace.slug].tabs.length;
    await page.locator(`[data-tab-id="${requirementTab.id}"]`).click();
    await page.getByTestId("requirement-flow-panel").filter({ visible: true }).first().getByText(/^(Open Issue|打开任务)$/).click();
    expect((await tabs()).state.byWorkspace[session.workspace.slug].tabs).toHaveLength(count);
    await writeFile(path.join(evidence, "desktop-nine-tabs.json"), JSON.stringify(await tabs(), null, 2));
    await page.screenshot({ path: path.join(evidence, "desktop-nine-existing.png") });
  } catch (error) {
    await writeFile(path.join(evidence, "desktop-nine-error.txt"), `${String(error)}\n\n${await page.locator("body").innerText()}`);
    await page.screenshot({ path: path.join(evidence, "desktop-nine-error.png") }).catch(() => {});
    throw error;
  } finally {
    await app.close();
  }
});

test("native desktop restores tabs, isolates workspaces and reconnects after Docker restart", async () => {
  test.skip(!enabled || process.env.MULTICA_NINE_PHASE !== "desktop-recovery", "explicit native recovery acceptance only");
  test.setTimeout(180_000);
  const session = JSON.parse(await readFile(path.join(evidence, "session.json"), "utf8"));
  expect(session.baseURL).toBe("http://127.0.0.1:18208");
  const previous = JSON.parse(await readFile(path.join(evidence, "desktop-nine-tabs.json"), "utf8"));
  const owner = new TestApiClient();
  await owner.login(session.email, "Requirement acceptance");
  owner.setWorkspaceId(session.workspace.id);
  const operation = JSON.parse(await readFile(path.join(evidence, "draft-fixed-result.json"), "utf8"));
  const detailResponse = await fetch(`${session.baseURL}/api/workspaces/${session.workspace.id}/requirements/${operation.requirement_id}`, {
    headers: { Authorization: `Bearer ${owner.getToken()}`, "X-Workspace-ID": session.workspace.id },
  });
  expect(detailResponse.ok).toBe(true);
  const detail = await detailResponse.json();
  const canonical = (url: string) => url.replace(`/issues/${detail.issueId}`, `/issues/${detail.issue.identifier}`);
  const second = (await owner.getWorkspaces()).find(workspace => workspace.slug === "requirement-nine-second")
    ?? await owner.createWorkspace("Requirement acceptance second", "requirement-nine-second");
  expect(second.id).not.toBe(session.workspace.id);
  expect(second.slug).toBe("requirement-nine-second");
  const { app, page } = await launchDesktop();
  const sockets: string[] = [];
  let requirementReads = 0;
  page.on("websocket", socket => sockets.push(new URL(socket.url()).pathname));
  page.on("response", response => {
    if (response.status() === 200 && new URL(response.url()).pathname.startsWith(`/api/workspaces/${session.workspace.id}/requirements/`)) requirementReads++;
  });
  try {
    const tabs = () => page.evaluate(() => JSON.parse(localStorage.getItem("multica_tabs") || "null"));
    const panel = page.locator('[data-testid="requirement-flow-panel"]:not([inert] *)').filter({ visible: true }).first();
    await expect(panel).toBeVisible({ timeout: 45_000 });
    const restored = await tabs();
    expect(restored.state.activeWorkspaceSlug).toBe(session.workspace.slug);
    expect(restored.state.byWorkspace[session.workspace.slug].tabs.map((tab: { url: string }) => canonical(tab.url)))
      .toEqual(previous.state.byWorkspace[session.workspace.slug].tabs.map((tab: { url: string }) => canonical(tab.url)));
    expect((await page.evaluate(() => window.daemonAPI.getPrefs())).autoStart).toBe(false);
    const openWorkspace = async (name: string) => {
      const label = page.getByText(name, { exact: true }).first();
      if ((await label.boundingBox())!.x < 0) await page.locator('[data-sidebar="trigger"]').click();
      await label.click();
    };
    await openWorkspace(session.workspace.name);
    await page.getByRole("menuitem").filter({ hasText: second.name }).click();
    await expect.poll(async () => (await tabs()).state.activeWorkspaceSlug).toBe(second.slug);
    await expect(page.locator('[data-testid="requirement-flow-panel"]:not([inert] *)').filter({ visible: true })).toHaveCount(0);
    await openWorkspace(second.name);
    await page.getByRole("menuitem").filter({ has: page.getByText(session.workspace.name, { exact: true }) }).click();
    await expect.poll(async () => (await tabs()).state.activeWorkspaceSlug).toBe(session.workspace.slug);
    await expect(panel).toBeVisible();
    const before = sockets.length;
    const readsBefore = requirementReads;
    const { promisify } = await import("node:util");
    const { execFile } = await import("node:child_process");
    await promisify(execFile)("wsl.exe", ["-d", "Ubuntu-24.04", "-e", "docker", "restart", "multica-nine-20261027-api"], { windowsHide: true, timeout: 45_000 });
    await expect.poll(() => sockets.length, { timeout: 45_000 }).toBeGreaterThan(before);
    await expect.poll(() => requirementReads, { timeout: 45_000 }).toBeGreaterThan(readsBefore);
    await expect(panel).toBeVisible();
    await writeFile(path.join(evidence, "desktop-nine-recovery.json"), JSON.stringify({ restoredTabs: true, workspaceSwitch: true,
      oldRequirementAbsentInSecondWorkspace: true, automaticDaemonStart: false, dockerRestart: true, websocketReconnected: true, requirementRefetched: true, sockets }, null, 2));
    await page.screenshot({ path: path.join(evidence, "desktop-nine-recovered.png") });
  } catch (error) {
    await writeFile(path.join(evidence, "desktop-nine-recovery-error.txt"), `${String(error)}\n\n${await page.locator("body").innerText()}`);
    await writeFile(path.join(evidence, "desktop-nine-recovery-panels.json"), JSON.stringify(await page.getByTestId("requirement-flow-panel").evaluateAll(elements => elements.map(el => ({rect:el.getBoundingClientRect().toJSON(),html:el.parentElement?.outerHTML.slice(0,3000)}))), null, 2));
    await page.screenshot({ path: path.join(evidence, "desktop-nine-recovery-error.png") }).catch(() => {});
    throw error;
  } finally {
    await app.close();
  }
});
