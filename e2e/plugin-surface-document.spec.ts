import { expect, test, type Page } from "@playwright/test";
import { buildSurfaceFrameDocument } from "../packages/views/plugins/surface-document";

/**
 * Real Chromium coverage for behavior jsdom does not implement: executing a
 * hosted document inside the sandboxed wrapper, nested frame lifecycle events,
 * and one-shot terminal messages from the wrapper to the host.
 */

const pluginURL = "https://plugin-content.example.test/plugin-surfaces/opaque";

async function routePluginScript(page: Page, script: string) {
  await page.route("https://plugin-content.example.test/**", async (route) => {
    await route.fulfill({
      contentType: "text/html",
      headers: { "Content-Security-Policy": "default-src 'none'; script-src 'unsafe-inline'" },
      body: `<!doctype html><script>${script}</script>`,
    });
  });
}

test.describe("plugin surface document (real Chromium, sandboxed srcdoc)", () => {
  test("a host-authored srcdoc reload is not reported as hostile navigation", async ({ page }) => {
    await routePluginScript(page, `
      const channel = new MessageChannel();
      parent.postMessage({
        type: "multica:plugin-bridge-connect",
        version: 2,
        challenge: "proof"
      }, "*", [channel.port1]);
    `);
    const first = buildSurfaceFrameDocument({ url: pluginURL, bridgeToken: "proof" });
    const reloaded = buildSurfaceFrameDocument({ url: `${pluginURL}?reload=1`, bridgeToken: "proof" });

    await page.setContent("<!doctype html><body></body>");
    await page.evaluate((srcdoc) => {
      const state = { ran: 0, loaded: 0, navigated: 0 };
      (window as unknown as { __surfaceState: typeof state }).__surfaceState = state;
      const frame = document.createElement("iframe");
      frame.id = "surface";
      frame.sandbox.add("allow-scripts");
      frame.sandbox.add("allow-same-origin");
      frame.addEventListener("load", () => { state.loaded++; });
      window.addEventListener("message", (event) => {
        if (event.source !== frame.contentWindow) return;
        const type = (event.data as { type?: string } | null)?.type;
        if (type === "multica:plugin-bridge-connect" && event.ports[0]) state.ran++;
        if (type === "multica:plugin-surface-navigated") state.navigated++;
      });
      frame.srcdoc = srcdoc;
      document.body.appendChild(frame);
    }, first);
    const readState = () => page.evaluate(() =>
      (window as unknown as { __surfaceState: { ran: number; loaded: number; navigated: number } }).__surfaceState,
    );

    await expect.poll(readState).toMatchObject({ ran: 1, loaded: 1, navigated: 0 });

    await page.evaluate((srcdoc) => {
      document.querySelector<HTMLIFrameElement>("#surface")!.srcdoc = srcdoc;
    }, reloaded);

    await expect.poll(readState).toMatchObject({ ran: 2, loaded: 2, navigated: 0 });
  });

  test("a second child navigation is reported to the host", async ({ page }) => {
    await routePluginScript(page, "");
    const wrapper = buildSurfaceFrameDocument({ url: pluginURL, bridgeToken: "proof" });

    await page.setContent("<!doctype html><body></body>");
    await page.evaluate((srcdoc) => {
      const state = { loaded: 0, navigated: 0 };
      (window as unknown as { __surfaceState: typeof state }).__surfaceState = state;
      const frame = document.createElement("iframe");
      frame.id = "surface";
      frame.sandbox.add("allow-scripts");
      frame.sandbox.add("allow-same-origin");
      frame.addEventListener("load", () => { state.loaded++; });
      window.addEventListener("message", (event) => {
        if (event.source !== frame.contentWindow) return;
        if ((event.data as { type?: string } | null)?.type === "multica:plugin-surface-navigated") {
          state.navigated++;
        }
      });
      frame.srcdoc = srcdoc;
      document.body.appendChild(frame);
    }, wrapper);
    const readState = () => page.evaluate(() =>
      (window as unknown as { __surfaceState: { loaded: number; navigated: number } }).__surfaceState,
    );

    await expect.poll(readState).toMatchObject({ loaded: 1, navigated: 0 });

    await page.evaluate((url) => {
      const frame = document.querySelector<HTMLIFrameElement>("#surface")!;
      const child = frame.contentDocument!.querySelector<HTMLIFrameElement>("iframe")!;
      child.src = url;
    }, `${pluginURL}?navigated=1`);

    await expect.poll(readState).toMatchObject({ loaded: 1, navigated: 1 });
  });

  test("reports a synchronous plugin error when the host listener is armed before srcdoc", async ({ page }) => {
    await routePluginScript(page, `
      parent.postMessage({ type: "multica:plugin-surface-error" }, "*");
    `);
    const documentWithError = buildSurfaceFrameDocument({ url: pluginURL, bridgeToken: "proof" });

    await page.setContent("<!doctype html><body></body>");
    await page.evaluate((srcdoc) => {
      const state = { errors: 0 };
      (window as unknown as { __surfaceState: typeof state }).__surfaceState = state;
      const frame = document.createElement("iframe");
      frame.id = "surface";
      frame.sandbox.add("allow-scripts");
      frame.sandbox.add("allow-same-origin");
      // This is a one-shot protocol: a listener mounted after the guest runs
      // has no delivery guarantee, so arm it before assigning srcdoc.
      window.addEventListener("message", (event) => {
        if (event.source !== frame.contentWindow) return;
        if ((event.data as { type?: string } | null)?.type === "multica:plugin-surface-error") {
          state.errors++;
        }
      });
      frame.srcdoc = srcdoc;
      document.body.appendChild(frame);
    }, documentWithError);

    await expect.poll(() => page.evaluate(() =>
      (window as unknown as { __surfaceState: { errors: number } }).__surfaceState.errors,
    )).toBe(1);
  });
});
