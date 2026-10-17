import { test, expect } from "@playwright/test";
import { TestApiClient } from "./fixtures";

// Verification for a packaged all-in-one build. Point it at the scratch
// deployment started from dist/build-*/docker-server:
//
//   PLAYWRIGHT_BASE_URL=http://localhost:13000 \
//   NEXT_PUBLIC_API_URL=http://localhost:18080 \
//   DATABASE_URL=postgres://multica:multica@localhost:15432/multica?sslmode=disable \
//   MULTICA_DEV_VERIFICATION_CODE= \
//   pnpm exec playwright test e2e/build-artifact-smoke.spec.ts
//
// The commit and screenshot path default to this build's values and can be
// overridden with ARTIFACT_SMOKE_COMMIT / ARTIFACT_SMOKE_SCREENSHOT.
const expectedCommit = process.env.ARTIFACT_SMOKE_COMMIT ?? "8b11ed2aa";
const screenshotPath =
  process.env.ARTIFACT_SMOKE_SCREENSHOT ??
  "dist/build-20261017-182645/web-login.png";
const apiBase = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:18080";

test("built all-in-one image loads login and proxies backend login and issue writes", async ({
  page,
  request,
}) => {
  const healthResponse = await request.get(`${apiBase}/health`);
  expect(healthResponse.status()).toBe(200);
  expect(await healthResponse.json()).toMatchObject({
    status: "ok",
    commit: expectedCommit,
  });

  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await page.goto("/login", { waitUntil: "networkidle" });
  await expect(page.getByRole("textbox", { name: "Email" })).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Continue", exact: true }),
  ).toBeDisabled();
  await page.screenshot({ path: screenshotPath, fullPage: true });
  expect(pageErrors).toEqual([]);

  const api = new TestApiClient();
  try {
    await api.login("artifact-smoke-0-6-4@localhost", "Artifact Smoke");
    const workspace = await api.ensureWorkspace(
      "Artifact Build Smoke",
      "artifact-build-smoke",
    );
    expect(workspace.id).toBeTruthy();
    const issue = await api.createIssue(
      "Built Docker images communicate successfully",
    );
    expect(issue.id).toBeTruthy();
    expect(issue.title).toBe("Built Docker images communicate successfully");
  } finally {
    await api.cleanup();
    await api.deleteWorkspace();
  }
});
