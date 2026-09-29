import { test, expect } from "@playwright/test";
import { TestApiClient } from "./fixtures";

test.use({ viewport: { width: 1440, height: 1000 } });

test("fork integrations survive the upstream settings reorganization", async ({ page }, testInfo) => {
  const api = new TestApiClient();
  const suffix = `${Date.now()}-${testInfo.workerIndex}`;
  await api.login(`fork-sync-${suffix}@localhost`, "Fork integration tester");
  const workspace = await api.ensureWorkspace("Fork integrations", `fork-sync-${suffix}`);
  await api.markUserOnboarded();

  try {
    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token);
      localStorage.setItem("multica:chat:isOpen", "false");
    }, api.getToken()!);

    // Existing links must still reach the retained workspace integrations.
    await page.goto(`/${workspace.slug}/settings?tab=integrations&integration=popo`, { waitUntil: "domcontentloaded" });
    await expect(page.getByRole("heading", { name: "POPO", exact: true })).toBeVisible();
    await expect(page.getByRole("link", { name: "Messaging", exact: true }).last()).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("popo-settings.png"), fullPage: true });

    await page.goto(`/${workspace.slug}/settings?tab=integrations&integration=yixiezuo`, { waitUntil: "domcontentloaded" });
    await expect(page.getByLabel("易协作 issue URL")).toBeVisible();
    await expect(page.getByRole("button", { name: "Read preview" })).toBeDisabled();
    await page.screenshot({ path: testInfo.outputPath("yixiezuo-settings.png"), fullPage: true });

    // This save goes to the real local backend; no Perforce server is contacted.
    await page.goto(`/${workspace.slug}/settings?tab=repositories`, { waitUntil: "domcontentloaded" });
    await page.getByRole("button", { name: "Add depot" }).click();
    await page.getByLabel("perforce.example.com:1666").fill("p4.example.com:1666");
    await page.getByLabel("//depot/project").fill("//depot/game");
    await page.getByLabel("//depot/project").blur();
    await expect(page.getByText("Perforce depots saved", { exact: true })).toBeVisible();
    await page.reload();
    await expect(page.getByLabel("perforce.example.com:1666")).toHaveValue("p4.example.com:1666");
    await expect(page.getByLabel("//depot/project")).toHaveValue("//depot/game");
    await page.screenshot({ path: testInfo.outputPath("perforce-settings.png"), fullPage: true });
  } finally {
    await api.deleteWorkspace();
  }
});
