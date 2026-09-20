// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

type MemberRole = "owner" | "admin" | "member";

const listingRef = vi.hoisted(() => ({
  current: {
    installations: [] as unknown[],
    configured: true,
    install_supported: true,
  },
}));
const bridgesRef = vi.hoisted(() => ({
  current: {
    data: {
      bridges: [] as unknown[],
      configured: true,
    },
    isError: false,
    error: undefined as unknown,
  },
}));
const statusRef = vi.hoisted(() => ({
  current: {
    data: {
      configured: true,
      protocol_version: 1,
      runtime_online: false,
      bridges: [] as unknown[],
    } as {
      configured: boolean;
      protocol_version: number;
      runtime_online: boolean;
      bridges: unknown[];
    } | undefined,
    isError: false,
    error: undefined as unknown,
  },
}));
const membersRef = vi.hoisted(() => ({
  current: [{ user_id: "user-1", role: "admin" as MemberRole }],
}));
const registerPopoBot = vi.hoisted(() => vi.fn());
const createPopoRegistration = vi.hoisted(() => vi.fn());
const getPopoRegistration = vi.hoisted(() => vi.fn());
const cancelPopoRegistration = vi.hoisted(() => vi.fn());
const createPopoBridgePairing = vi.hoisted(() => vi.fn());
const revokePopoBridge = vi.hoisted(() => vi.fn());
const ApiError = vi.hoisted(() => {
  return class ApiError extends Error {
    status: number;
    statusText: string;
    body?: unknown;
    constructor(message: string, status: number, statusText: string, body?: unknown) {
      super(message);
      this.name = "ApiError";
      this.status = status;
      this.statusText = statusText;
      this.body = body;
    }
  };
});

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { queryKey: unknown[]; enabled?: boolean }) => {
    if (opts.enabled === false) return { data: undefined, isLoading: false, isError: false };
    const key = JSON.stringify(opts.queryKey ?? []);
    if (key.includes("members")) {
      return { data: membersRef.current, isLoading: false, isError: false };
    }
    if (key.includes("status")) {
      return {
        data: statusRef.current.data,
        isLoading: false,
        isError: statusRef.current.isError,
        error: statusRef.current.error,
      };
    }
    if (key.includes("bridges")) {
      return {
        data: bridgesRef.current.data,
        isLoading: false,
        isError: bridgesRef.current.isError,
        error: bridgesRef.current.error,
      };
    }
    if (key.includes("installations") || key.includes("popo")) {
      return { data: listingRef.current, isLoading: false, isError: false };
    }
    return { data: undefined, isLoading: false };
  },
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  queryOptions: <T,>(opts: T) => opts,
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/auth", () => {
  const useAuthStore = Object.assign(
    (selector: (s: { user: { id: string } }) => unknown) =>
      selector({ user: { id: "user-1" } }),
    { getState: () => ({ user: { id: "user-1" } }) },
  );
  return { useAuthStore };
});
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: vi.fn() }),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getAgentName: () => "Agent" }),
}));
vi.mock("@multica/core/popo", () => ({
  popoKeys: {
    all: (wsId: string) => ["popo", wsId],
    installations: (wsId: string) => ["popo", wsId, "installations"],
    bridges: (wsId: string) => ["popo", wsId, "bridges"],
    status: (wsId: string) => ["popo", wsId, "status"],
  },
  popoInstallationsOptions: (wsId: string) => ({
    queryKey: ["popo", wsId, "installations"],
    queryFn: vi.fn(),
  }),
  popoBridgesOptions: (wsId: string) => ({
    queryKey: ["popo", wsId, "bridges"],
    queryFn: vi.fn(),
  }),
  popoStatusOptions: (wsId: string) => ({
    queryKey: ["popo", wsId, "status"],
    queryFn: vi.fn(),
  }),
}));
vi.mock("@multica/core/api", () => ({
  ApiError,
  errorCode: (err: unknown) => {
    if (err instanceof ApiError && err.body && typeof err.body === "object") {
      const code = (err.body as { code?: unknown }).code;
      return typeof code === "string" ? code : undefined;
    }
    return undefined;
  },
  api: {
    registerPopoBot,
    createPopoRegistration,
    getPopoRegistration,
    cancelPopoRegistration,
    deletePopoInstallation: vi.fn(),
    createPopoBridgePairing,
    revokePopoBridge,
    getBaseUrl: () => "https://multica.example",
  },
}));
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() },
}));
vi.mock("react-qr-code", () => {
  const QrStub = ({ value }: { value: string }) => (
    <span data-testid="qr-code" data-value={value} />
  );
  return { QRCode: QrStub, default: QrStub };
});
vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => <span>avatar</span>,
}));

import { PopoAgentBindButton, PopoTab, popoPairCommand } from "./popo-tab";

const TEST_RESOURCES = {
  en: { common: enCommon, settings: enSettings },
};

function renderUI(ui: ReactNode) {
  return render(<I18nProvider locale="en" resources={TEST_RESOURCES}>{ui}</I18nProvider>);
}

function idleOnlineBridge() {
  return {
    id: "b1",
    hostname: "WIN-1",
    status: "active",
    online: true,
    last_heartbeat_at: "2026-09-19T12:00:00Z",
    created_at: "2026-09-19T11:00:00Z",
    robots: [
      { robot_id: "idle-1", display_name: "Idle Bot", connected: true, occupied_by: null },
      { robot_id: "held-1", display_name: "Held Bot", connected: true, occupied_by: "multica" },
      { robot_id: "busy-1", display_name: "Busy Bot", connected: true, occupied_by: "dj01bot" },
      { robot_id: "down-1", display_name: "Down Bot", connected: false, occupied_by: null },
    ],
  };
}

beforeEach(() => {
  listingRef.current = {
    installations: [],
    configured: true,
    install_supported: true,
  };
  bridgesRef.current = {
    data: { bridges: [], configured: true },
    isError: false,
    error: undefined,
  };
  statusRef.current = {
    data: {
      configured: true,
      protocol_version: 1,
      runtime_online: false,
      bridges: [],
    },
    isError: false,
    error: undefined,
  };
  membersRef.current = [{ user_id: "user-1", role: "admin" }];
  registerPopoBot.mockReset();
  createPopoRegistration.mockReset();
  getPopoRegistration.mockReset();
  cancelPopoRegistration.mockReset();
  createPopoBridgePairing.mockReset();
  revokePopoBridge.mockReset();
});

describe("popoPairCommand", () => {
  it("emits the frozen Windows pair command", () => {
    expect(popoPairCommand("https://multica.example", "PAIRCODE123")).toBe(
      "python -m nanobot.multica_bridge pair --server https://multica.example --pairing-code PAIRCODE123",
    );
  });
});

describe("PopoTab", () => {
  it("explains how to enable POPO when MULTICA_POPO_ENABLED is off", () => {
    listingRef.current.configured = false;
    listingRef.current.install_supported = false;
    bridgesRef.current.data.configured = false;
    renderUI(<PopoTab />);
    expect(screen.getByText(/POPO integration not enabled/i)).toBeTruthy();
    expect(screen.getByText("MULTICA_POPO_ENABLED=true")).toBeTruthy();
    expect(screen.queryByText("MULTICA_POPO_SECRET_KEY")).toBeNull();
  });

  it("treats a 503 list response as not enabled", () => {
    bridgesRef.current = {
      data: { bridges: [], configured: true },
      isError: true,
      error: new ApiError("popo integration not configured", 503, "Service Unavailable", {
        code: "popo_not_configured",
      }),
    };
    renderUI(<PopoTab />);
    expect(screen.getByText(/POPO integration not enabled/i)).toBeTruthy();
    expect(screen.getByText("MULTICA_POPO_ENABLED=true")).toBeTruthy();
  });

  it("asks the admin to connect from an agent when no robots exist", () => {
    renderUI(<PopoTab />);
    expect(screen.getByText(/No robots connected yet/i)).toBeTruthy();
    expect(screen.getByText("Connect POPO")).toBeTruthy();
  });

  it("shows a pairing code once and clears it when the dialog closes", async () => {
    createPopoBridgePairing.mockResolvedValue({
      id: "p1",
      pairing_code: "PAIRCODE123",
      expires_at: "2099-01-01T00:00:00Z",
      ttl_seconds: 900,
    });
    renderUI(<PopoTab />);
    await userEvent.click(screen.getByTestId("popo-create-pairing"));
    expect(await screen.findByTestId("popo-pairing-dialog")).toBeTruthy();
    expect(screen.getByTestId("popo-pairing-code").textContent).toBe("PAIRCODE123");
    expect(screen.getByTestId("popo-pairing-command").textContent).toBe(
      "python -m nanobot.multica_bridge pair --server https://multica.example --pairing-code PAIRCODE123",
    );
    await userEvent.click(screen.getByTestId("popo-pairing-close"));
    expect(screen.queryByTestId("popo-pairing-dialog")).toBeNull();
    expect(screen.queryByText("PAIRCODE123")).toBeNull();
  });

  it("lists host occupancy and lets an admin revoke a bridge", async () => {
    bridgesRef.current.data.bridges = [idleOnlineBridge()];
    renderUI(<PopoTab />);
    expect(screen.getByText("WIN-1")).toBeTruthy();
    expect(screen.getByText("Online")).toBeTruthy();
    expect(screen.getByText(/Idle Bot · Idle/)).toBeTruthy();
    expect(screen.getByText(/Busy Bot · Held by dj01bot/)).toBeTruthy();
    await userEvent.click(screen.getByTestId("popo-revoke-bridge"));
    expect(screen.getByText(/Revoke this Windows host/i)).toBeTruthy();
    expect(
      screen.getByText(/This host will stop sending and receiving POPO messages/i),
    ).toBeTruthy();
  });

  it("renders separate host, POPO, runtime, and backlog counters", () => {
    bridgesRef.current.data.bridges = [idleOnlineBridge()];
    statusRef.current.data = {
      configured: true,
      protocol_version: 1,
      runtime_online: true,
      bridges: [
        {
          id: "b1",
          hostname: "WIN-1",
          online: true,
          last_heartbeat_at: "2026-09-19T12:00:00Z",
          popo_connected: true,
          robots: idleOnlineBridge().robots,
          inbound_backlog: 2,
          outbound_backlog: 3,
          unknown_deliveries: 1,
        },
      ],
    };
    renderUI(<PopoTab />);
    expect(screen.getByTestId("popo-diag-host").textContent).toMatch(/Online/i);
    expect(screen.getByTestId("popo-diag-popo").textContent).toMatch(/POPO connected/i);
    expect(screen.getByTestId("popo-diag-runtime").textContent).toMatch(/Runtime online/i);
    expect(screen.getByTestId("popo-diag-inbound").textContent).toMatch(/Inbound backlog 2/);
    expect(screen.getByTestId("popo-diag-outbound").textContent).toMatch(/Outbound backlog 3/);
    expect(screen.getByTestId("popo-diag-unknown").textContent).toMatch(/Unknown deliveries 1/);
    expect(screen.queryByText(/healthy/i)).toBeNull();
  });
});

describe("PopoAgentBindButton", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("lists idle robots only and binds without a webhook URL", async () => {
    bridgesRef.current.data.bridges = [idleOnlineBridge()];
    registerPopoBot.mockResolvedValue({
      id: "inst-1",
      status: "active",
      robot_id: "idle-1",
    });
    renderUI(<PopoAgentBindButton agentId="agent-1" agentName="Bot" />);
    await userEvent.click(screen.getByTestId("popo-agent-connect"));
    expect(screen.getByTestId("popo-connect-dialog")).toBeTruthy();
    expect(screen.queryByTestId("popo-webhook-url")).toBeNull();
    const robotSelect = await screen.findByTestId("popo-robot-select") as HTMLSelectElement;
    await waitFor(() => expect(robotSelect.value).toBe("idle-1"));
    const values = [...robotSelect.options].map((option) => option.value);
    expect(values).toEqual(["idle-1", "held-1"]);
    await userEvent.click(screen.getByTestId("popo-connect-submit"));
    expect(registerPopoBot).toHaveBeenCalledWith("ws-1", "agent-1", {
      bridge_id: "b1",
      robot_id: "idle-1",
      robot_name: "",
    });
  });

  it("shows Scan to create next to idle bind", async () => {
    bridgesRef.current.data.bridges = [idleOnlineBridge()];
    renderUI(<PopoAgentBindButton agentId="agent-1" />);
    await userEvent.click(screen.getByTestId("popo-agent-connect"));
    expect(screen.getByTestId("popo-scan-to-create")).toBeTruthy();
    expect(screen.getByTestId("popo-connect-submit")).toBeTruthy();
  });

  it("polls scan status until success and keeps the bind path available before scanning", async () => {
    bridgesRef.current.data.bridges = [idleOnlineBridge()];
    createPopoRegistration.mockResolvedValue({
      id: "reg-1",
      status: "awaiting_scan",
      qr_url: "https://popo.example/qr",
      robot_id: "",
      installation_id: "",
      error_reason: "",
      poll_interval_seconds: 0.05,
    });
    getPopoRegistration.mockResolvedValue({
      id: "reg-1",
      status: "success",
      qr_url: "https://popo.example/qr",
      robot_id: "new-bot",
      installation_id: "inst-9",
      error_reason: "",
      poll_interval_seconds: 0.05,
    });
    renderUI(<PopoAgentBindButton agentId="agent-1" />);
    await userEvent.click(screen.getByTestId("popo-agent-connect"));
    expect(screen.getByTestId("popo-connect-submit")).toBeEnabled();
    await userEvent.click(screen.getByTestId("popo-scan-to-create"));
    await waitFor(() => expect(createPopoRegistration).toHaveBeenCalledWith("ws-1", "agent-1", {
      bridge_id: "b1",
    }));
    expect(registerPopoBot).not.toHaveBeenCalled();
    expect(await screen.findByTestId("qr-code")).toBeTruthy();
    expect(screen.getByTestId("qr-code").getAttribute("data-value")).toBe(
      "https://popo.example/qr",
    );
    await waitFor(() => expect(getPopoRegistration).toHaveBeenCalledWith("ws-1", "reg-1"));
    await waitFor(() => expect(screen.getByText("Robot created.")).toBeTruthy());
    await waitFor(
      () => expect(screen.queryByTestId("popo-connect-dialog")).toBeNull(),
      { timeout: 3000 },
    );
  });

  it("tells the agent owner to pair a host when none are online", async () => {
    bridgesRef.current.data.bridges = [
      { ...idleOnlineBridge(), online: false },
    ];
    renderUI(<PopoAgentBindButton agentId="agent-1" agentOwnerId="user-1" />);
    await userEvent.click(screen.getByTestId("popo-agent-connect"));
    expect(screen.getByText(/Pair a Windows host in Settings first/i)).toBeTruthy();
    expect(screen.queryByTestId("popo-robot-select")).toBeNull();
    expect(screen.getByTestId("popo-connect-submit")).toBeDisabled();
  });

  it("explains occupancy when the host has no idle robots", async () => {
    const bridge = idleOnlineBridge();
    bridge.robots = [
      { robot_id: "busy-1", display_name: "Busy Bot", connected: true, occupied_by: "sparse" },
    ];
    bridgesRef.current.data.bridges = [bridge];
    renderUI(<PopoAgentBindButton agentId="agent-1" />);
    await userEvent.click(screen.getByTestId("popo-agent-connect"));
    expect(
      screen.getByText(/held by dj01bot, Sparse, or another agent/i),
    ).toBeTruthy();
    expect(screen.queryByTestId("popo-robot-select")).toBeNull();
  });

  it("shows Connect for the agent owner who is not a workspace admin", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    renderUI(<PopoAgentBindButton agentId="agent-1" agentOwnerId="user-1" />);
    expect(screen.getByTestId("popo-agent-connect")).toBeTruthy();
  });

  it("hides Connect for a member who does not own the agent", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    renderUI(<PopoAgentBindButton agentId="agent-1" agentOwnerId="user-2" />);
    expect(screen.queryByTestId("popo-agent-connect")).toBeNull();
  });

  it("shows the connected badge when this agent already has a robot", () => {
    listingRef.current = {
      installations: [
        { id: "inst-1", agent_id: "agent-1", status: "active", robot_id: "default" },
      ],
      configured: true,
      install_supported: true,
    };
    renderUI(<PopoAgentBindButton agentId="agent-1" />);
    expect(screen.getByTestId("popo-agent-bot-connected")).toBeTruthy();
    expect(screen.queryByTestId("popo-agent-connect")).toBeNull();
  });
});
