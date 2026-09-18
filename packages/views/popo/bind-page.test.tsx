import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { type ReactNode } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";

const TEST_RESOURCES = { en: { common: enCommon } };

const mockAuthState = vi.hoisted(() => ({
  user: null as { id: string; email: string } | null,
  isLoading: false,
}));
const mockNavigatePush = vi.hoisted(() => vi.fn());
const mockRedeemToken = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/auth", () => {
  const useAuthStore = Object.assign(
    (selector?: (state: typeof mockAuthState) => unknown) =>
      selector ? selector(mockAuthState) : mockAuthState,
    { getState: () => mockAuthState },
  );
  return { useAuthStore };
});

vi.mock("../navigation/context", () => ({
  useNavigation: () => ({ push: mockNavigatePush }),
  useOptionalNavigation: () => ({ push: mockNavigatePush }),
}));

vi.mock("@multica/core/api", () => ({
  api: { redeemPopoBindingToken: mockRedeemToken },
}));

import { PopoBindPage } from "./bind-page";

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function renderPage(token: string | null) {
  return render(<PopoBindPage token={token} />, { wrapper: I18nWrapper });
}

describe("PopoBindPage", () => {
  beforeEach(() => {
    mockAuthState.user = null;
    mockAuthState.isLoading = false;
    mockNavigatePush.mockReset();
    mockRedeemToken.mockReset();
  });

  it("requires sign-in before redeeming", () => {
    renderPage("tok123");
    expect(screen.getByRole("button", { name: /sign in/i })).toBeInTheDocument();
    expect(mockRedeemToken).not.toHaveBeenCalled();
  });

  it("preserves the token in the login next parameter", () => {
    renderPage("token with+/reserved");
    fireEvent.click(screen.getByRole("button", { name: /sign in/i }));
    const destination = mockNavigatePush.mock.calls[0]?.[0] as string;
    expect(decodeURIComponent(destination.split("next=")[1] ?? "")).toBe(
      "/popo/bind?token=token%20with%2B%2Freserved",
    );
  });

  it("redeems immediately when signed in and shows success", async () => {
    mockAuthState.user = { id: "u1", email: "u@example.com" };
    mockRedeemToken.mockResolvedValue({
      workspace_id: "ws1",
      installation_id: "inst1",
      popo_user_id: "yujian01@corp.netease.com",
    });
    renderPage("tok123");
    await waitFor(() => expect(mockRedeemToken).toHaveBeenCalledWith("tok123"));
    await waitFor(() => expect(screen.getByText(/you're linked/i)).toBeInTheDocument());
  });

  it("shows the missing-token error without calling the API", () => {
    renderPage(null);
    expect(screen.getByText(/missing its token/i)).toBeInTheDocument();
    expect(mockRedeemToken).not.toHaveBeenCalled();
  });
});
