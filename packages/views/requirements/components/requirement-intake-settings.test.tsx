// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { PopoInstallationSchema } from "@multica/core/api/schemas";
import { renderWithI18n } from "../../test/i18n";
import { RequirementIntakeSettings } from "./requirement-intake-settings";

const save = vi.hoisted(() => vi.fn());
const flag = vi.hoisted(() => ({ enabled: true }));
vi.mock("@multica/core/api", () => ({ api: { setPopoRequirementIntake: save } }));
vi.mock("@multica/core/config", () => ({ useFeatureEnabled: () => flag.enabled }));

const installation = PopoInstallationSchema.parse({ id: "install-1", status: "active" });

function renderSettings(template = "") {
  const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <RequirementIntakeSettings wsId="ws-1" installation={{ ...installation, requirement_template_url: template }} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  save.mockReset();
  flag.enabled = true;
});

describe("POPO requirement intake settings", () => {
  it("makes source creation and Chat approval visible before saving", async () => {
    save.mockResolvedValue({ ...installation, requirement_template_url: "https://dj01.pm.netease.com/issues/108402" });
    const user = userEvent.setup();
    renderSettings();
    const input = screen.getByRole("textbox", { name: "POPO requirement template" });
    expect(input).toHaveAccessibleDescription(/create 易协作 drafts.*approve in Multica Chat/);
    await user.type(input, "https://dj01.pm.netease.com/issues/108402");
    expect(save).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalledWith("ws-1", "install-1", "https://dj01.pm.netease.com/issues/108402"));
    expect(await screen.findByRole("status")).toHaveTextContent("Saved");
  });

  it("keeps the entered URL and shows a failed response for retry", async () => {
    save.mockResolvedValue({ id: "", status: "revoked" });
    const user = userEvent.setup();
    renderSettings();
    await user.type(screen.getByRole("textbox"), "https://dj01.pm.netease.com/issues/108402");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not save");
    expect(screen.getByRole("textbox")).toHaveValue("https://dj01.pm.netease.com/issues/108402");
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
  });

  it("allows disabling an existing entry even while its feature is off", async () => {
    flag.enabled = false;
    save.mockResolvedValue({ ...installation, requirement_template_url: "" });
    const user = userEvent.setup();
    renderSettings("https://dj01.pm.netease.com/issues/108402");
    await user.clear(screen.getByRole("textbox"));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalledWith("ws-1", "install-1", ""));
  });

  it("hides an unconfigured entry when its feature is off", () => {
    flag.enabled = false;
    renderSettings();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });
});
