// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

const listingRef = vi.hoisted(() => ({
  current: {
    installations: [] as unknown[],
    configured: true,
    install_supported: true,
  },
}));
const registerPopoBot = vi.hoisted(() => vi.fn());

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { queryKey: unknown[]; enabled?: boolean }) => {
    if (opts.enabled === false) return { data: undefined };
    const key = JSON.stringify(opts.queryKey);
    if (key.includes("members")) {
      return { data: [{ user_id: "user-1", role: "admin" }] };
    }
    return { data: listingRef.current, isLoading: false, isError: false };
  },
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  queryOptions: <T,>(opts: T) => opts,
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (s: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: vi.fn() }),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getAgentName: () => "Agent" }),
}));
vi.mock("@multica/core/popo", () => ({
  popoKeys: { installations: (wsId: string) => ["popo", "installations", wsId] },
  popoInstallationsOptions: (wsId: string) => ({
    queryKey: ["popo", "installations", wsId],
    queryFn: vi.fn(),
  }),
}));
vi.mock("@multica/core/api", () => ({
  api: { registerPopoBot, deletePopoInstallation: vi.fn() },
}));
vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => <span>avatar</span>,
}));

import { PopoAgentBindButton, PopoTab } from "./popo-tab";

const TEST_RESOURCES = {
  en: { common: enCommon, settings: enSettings },
};

function renderUI(ui: ReactNode) {
  return render(<I18nProvider locale="en" resources={TEST_RESOURCES}>{ui}</I18nProvider>);
}

beforeEach(() => {
  listingRef.current = {
    installations: [],
    configured: true,
    install_supported: true,
  };
  registerPopoBot.mockReset();
});

describe("PopoTab", () => {
  it("explains how to enable POPO when the secret key is missing", () => {
    listingRef.current.configured = false;
    listingRef.current.install_supported = false;
    renderUI(<PopoTab />);
    expect(screen.getByText(/POPO integration not enabled/i)).toBeTruthy();
    expect(screen.getByText("MULTICA_POPO_SECRET_KEY")).toBeTruthy();
  });

  it("asks the admin to connect from an agent when no robots exist", () => {
    renderUI(<PopoTab />);
    expect(screen.getByText(/No robots connected yet/i)).toBeTruthy();
    expect(screen.getByText("Connect POPO")).toBeTruthy();
  });
});

describe("PopoAgentBindButton", () => {
  it("submits a loopback webhook and empty robot id", async () => {
    registerPopoBot.mockResolvedValue({
      id: "inst-1",
      status: "active",
      robot_id: "default",
    });
    renderUI(<PopoAgentBindButton agentId="agent-1" agentName="Bot" />);
    await userEvent.click(screen.getByTestId("popo-agent-connect"));
    expect(screen.getByTestId("popo-connect-dialog")).toBeTruthy();
    await userEvent.click(screen.getByTestId("popo-connect-submit"));
    expect(registerPopoBot).toHaveBeenCalledWith("ws-1", "agent-1", {
      robot_id: "",
      robot_name: "",
      webhook_url: "http://127.0.0.1:28792",
    });
  });

  it("shows the connected badge when this agent already has a robot", () => {
    listingRef.current.installations = [
      { id: "inst-1", agent_id: "agent-1", status: "active", robot_id: "default" },
    ];
    renderUI(<PopoAgentBindButton agentId="agent-1" />);
    expect(screen.getByTestId("popo-agent-bot-connected")).toBeTruthy();
    expect(screen.queryByTestId("popo-agent-connect")).toBeNull();
  });
});
