import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { renderWithI18n } from "../../test/i18n";

const state = vi.hoisted(() => ({ search: "tab=channels", replace: vi.fn() }));
vi.mock("../../navigation/context", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../navigation/context")>()),
  useNavigation: () => ({ pathname: "/acme/settings", searchParams: new URLSearchParams(state.search), replace: state.replace, push: vi.fn() }),
}));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ agentDetail: (id: string) => `/acme/agents/${id}` }),
  useCurrentWorkspace: () => ({ id: "ws-1", name: "Acme", slug: "acme" }),
}));
vi.mock("@tanstack/react-query", () => ({
  queryOptions: <T,>(options: T) => options,
  useQuery: () => ({ data: [], isSuccess: true }),
}));
vi.mock("./use-messaging-channels", () => ({
  useMessagingChannels: () => ({
    wsId: "ws-1", isAdmin: true, connections: [], available: () => true,
    queries: Object.fromEntries(["lark", "slack", "dingtalk", "wecom", "telegram", "popo"].map((channel) => [channel, { data: { configured: true }, isPending: false, isError: false }])),
  }),
}));
vi.mock("./popo-tab", () => ({ PopoTab: () => <div>POPO bridge and member bindings</div> }));
vi.mock("./channel-connect-dialog", () => ({ ChannelConnectDialog: () => null }));
vi.mock("./channel-disconnect-dialog", () => ({ ChannelDisconnectDialog: () => null }));
import { ChannelsTab } from "./channels-tab";

beforeEach(() => { state.search = "tab=channels"; state.replace.mockClear(); });
describe("POPO in the messaging settings", () => {
  it.each(["tab=channels&integration=popo", "tab=integrations&integration=popo", "tab=popo"])("keeps bridge and member controls reachable through %s", (search) => {
    state.search = search;
    renderWithI18n(<ChannelsTab />);
    expect(screen.getByText("POPO bridge and member bindings")).toBeInTheDocument();
    expect(state.replace).not.toHaveBeenCalled();
  });
  it("links the POPO overview to its bridge and member settings", () => {
    renderWithI18n(<ChannelsTab />);
    expect(screen.getByRole("link", { name: "POPO" })).toHaveAttribute("href", "/acme/settings?tab=channels&integration=popo");
  });
});
