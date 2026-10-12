import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type { P4Changelist, P4ChangelistStatus, ProjectResource } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

const apiMock = vi.hoisted(() => ({
  listIssueP4Changelists: vi.fn(),
  linkIssueP4Changelist: vi.fn(),
  unlinkIssueP4Changelist: vi.fn(),
  listProjectResources: vi.fn(),
}));
vi.mock("@multica/core/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/api")>()),
  api: apiMock,
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
const copyText = vi.hoisted(() => vi.fn());
vi.mock("@multica/ui/lib/clipboard", () => ({ copyText }));

import { P4ChangelistsSection } from "./p4-changelists-section";

function changelist(overrides: Partial<P4Changelist> = {}): P4Changelist {
  return {
    id: "cl-1",
    workspace_id: "ws-1",
    port: "perforce.example.com:1666",
    number: 795095,
    original_number: 795095,
    status: "shelved",
    client: "dj-win",
    p4_user: "yujian",
    description: "refs #12345 fix the thing",
    ticket: "12345",
    files_count: 2,
    exclusive_files: [],
    warnings: [],
    swarm_review_id: null,
    swarm_review_number: null,
    swarm_review_url: null,
    swarm_review_state: null,
    task_id: "task-1",
    link_source: "task",
    submitted_at: null,
    created_at: "",
    updated_at: "",
    ...overrides,
  };
}

function depot(port: string, path = "//depot/game"): ProjectResource {
  return {
    id: `res-${port}-${path}`,
    workspace_id: "ws-1",
    project_id: "project-1",
    resource_type: "perforce_depot",
    resource_ref: { port, depot: path },
    label: null,
    position: 0,
    created_at: "",
    created_by: null,
  };
}

function renderSection(projectId: string | null = "project-1") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider resources={TEST_RESOURCES} locale="en">
        <P4ChangelistsSection issueId="issue-1" projectId={projectId} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("P4ChangelistsSection", () => {
  beforeEach(() => {
    apiMock.listIssueP4Changelists.mockReset().mockResolvedValue({ changelists: [] });
    apiMock.linkIssueP4Changelist.mockReset().mockResolvedValue(changelist());
    apiMock.unlinkIssueP4Changelist.mockReset().mockResolvedValue(undefined);
    apiMock.listProjectResources.mockReset().mockResolvedValue({ resources: [depot("perforce.example.com:1666")] });
    copyText.mockReset().mockResolvedValue(true);
  });

  it("stays hidden for an issue that is neither on a Perforce project nor carries changelists", async () => {
    apiMock.listProjectResources.mockResolvedValue({
      resources: [
        {
          ...depot("perforce.example.com:1666"),
          resource_type: "github_repo",
          resource_ref: { url: "https://github.com/acme/widget" },
        } as ProjectResource,
      ],
    });
    const { container } = renderSection();
    await waitFor(() => expect(apiMock.listIssueP4Changelists).toHaveBeenCalled());
    await waitFor(() => expect(apiMock.listProjectResources).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });

  it("stays hidden for an issue without a project", async () => {
    const { container } = renderSection(null);
    await waitFor(() => expect(apiMock.listIssueP4Changelists).toHaveBeenCalled());
    expect(apiMock.listProjectResources).not.toHaveBeenCalled();
    expect(container).toBeEmptyDOMElement();
  });

  // A detached depot must not hide files nobody else can edit.
  it("keeps showing changelists after the depot leaves the project, without offering a new link", async () => {
    apiMock.listProjectResources.mockResolvedValue({ resources: [] });
    apiMock.listIssueP4Changelists.mockResolvedValue({ changelists: [changelist()] });
    renderSection();
    expect(await screen.findByText("CL 795095")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Link changelist" })).toBeNull();
  });

  it("says so when a Perforce project has no changelists yet", async () => {
    renderSection();
    expect(await screen.findByText("No changelists")).toBeInTheDocument();
  });

  it.each([
    ["pending", "Pending"],
    ["shelved", "Shelved"],
    ["submitted", "Submitted"],
    ["deleted", "Deleted"],
  ] as [P4ChangelistStatus, string][])("labels a %s changelist", async (status, label) => {
    apiMock.listIssueP4Changelists.mockResolvedValue({ changelists: [changelist({ status })] });
    renderSection();
    expect(await screen.findByText(label)).toBeInTheDocument();
    expect(screen.getByText("2 files")).toBeInTheDocument();
  });

  it("shows the number a submit produced, with the original on hover", async () => {
    apiMock.listIssueP4Changelists.mockResolvedValue({
      changelists: [changelist({ status: "submitted", number: 795115, original_number: 795095 })],
    });
    renderSection();
    const number = await screen.findByText("CL 795115");
    expect(number).toHaveAttribute("title", "Submitted as CL 795115, opened as CL 795095");
    expect(screen.queryByText("CL 795095")).toBeNull();
  });

  it("links to the Swarm review and names its state", async () => {
    apiMock.listIssueP4Changelists.mockResolvedValue({
      changelists: [
        changelist({
          swarm_review_number: 123,
          swarm_review_url: "https://swarm.example.com/reviews/123",
          swarm_review_state: "needsReview",
        }),
      ],
    });
    renderSection();
    expect(await screen.findByRole("link", { name: "Swarm #123" })).toHaveAttribute(
      "href",
      "https://swarm.example.com/reviews/123",
    );
    expect(screen.getByText("Needs review")).toBeInTheDocument();
  });

  it("reads a committed Swarm review as submitted, not merged", async () => {
    apiMock.listIssueP4Changelists.mockResolvedValue({
      changelists: [changelist({ status: "submitted", swarm_review_number: 7, swarm_review_state: "committed" })],
    });
    renderSection();
    await screen.findByText("Swarm #7");
    expect(screen.queryByText("Merged")).toBeNull();
  });

  it("warns about held exclusive files, stale files and a missing ticket", async () => {
    apiMock.listIssueP4Changelists.mockResolvedValue({
      changelists: [
        changelist({
          ticket: "",
          exclusive_files: ["//depot/game/Content/A.uasset", "//depot/game/Content/B.uasset"],
          warnings: [{ kind: "not_head", files: ["//depot/game/Content/C.uasset"] }],
        }),
      ],
    });
    renderSection();
    expect(await screen.findByText("Holds 2 exclusive files open")).toBeInTheDocument();
    expect(screen.getByText("Files not at head")).toHaveAttribute(
      "title",
      "//depot/game/Content/C.uasset",
    );
    expect(screen.getByText("Description has no 易协作 ticket")).toBeInTheDocument();
  });

  it("shows a warning kind it has never heard of as the server sent it", async () => {
    apiMock.listIssueP4Changelists.mockResolvedValue({
      changelists: [changelist({ warnings: [{ kind: "moon_phase", files: [] }] })],
    });
    renderSection();
    expect(await screen.findByText("moon_phase")).toBeInTheDocument();
  });

  it("copies the changelist number", async () => {
    apiMock.listIssueP4Changelists.mockResolvedValue({ changelists: [changelist({ number: 795115 })] });
    renderSection();
    fireEvent.click(await screen.findByRole("button", { name: "Copy CL number" }));
    await waitFor(() => expect(copyText).toHaveBeenCalledWith("795115"));
  });

  it("links a changelist by number on a single-depot project", async () => {
    renderSection();
    fireEvent.click(await screen.findByRole("button", { name: "Link changelist" }));
    fireEvent.change(await screen.findByRole("textbox", { name: "CL number" }), {
      target: { value: "795115" },
    });
    // One depot needs no choice: the server resolves the port from the project.
    expect(screen.queryByRole("combobox", { name: "Depot" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Link" }));
    await waitFor(() =>
      expect(apiMock.linkIssueP4Changelist).toHaveBeenCalledWith("issue-1", { number: 795115 }),
    );
  });

  it("asks which depot when the project has several, and sends that port", async () => {
    apiMock.listProjectResources.mockResolvedValue({
      resources: [depot("perforce.example.com:1666"), depot("other.example.com:1666", "//depot/tools")],
    });
    renderSection();
    fireEvent.click(await screen.findByRole("button", { name: "Link changelist" }));
    fireEvent.change(await screen.findByRole("textbox", { name: "CL number" }), {
      target: { value: "42" },
    });
    expect(await screen.findByLabelText("Depot")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Link" }));
    await waitFor(() =>
      expect(apiMock.linkIssueP4Changelist).toHaveBeenCalledWith("issue-1", {
        number: 42,
        port: "perforce.example.com:1666",
      }),
    );
  });

  it("sends the shared port when several depots sit on one server", async () => {
    apiMock.listProjectResources.mockResolvedValue({
      resources: [
        depot("perforce.example.com:1666", "//depot/game"),
        depot("perforce.example.com:1666", "//depot/tools"),
      ],
    });
    renderSection();
    fireEvent.click(await screen.findByRole("button", { name: "Link changelist" }));
    fireEvent.change(await screen.findByRole("textbox", { name: "CL number" }), {
      target: { value: "42" },
    });
    expect(screen.queryByRole("combobox", { name: "Depot" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Link" }));
    await waitFor(() =>
      expect(apiMock.linkIssueP4Changelist).toHaveBeenCalledWith("issue-1", {
        number: 42,
        port: "perforce.example.com:1666",
      }),
    );
  });

  it("keeps a non-numeric changelist number out of the request", async () => {
    renderSection();
    fireEvent.click(await screen.findByRole("button", { name: "Link changelist" }));
    fireEvent.change(await screen.findByRole("textbox", { name: "CL number" }), {
      target: { value: "default" },
    });
    expect(screen.getByRole("button", { name: "Link" })).toBeDisabled();
    expect(apiMock.linkIssueP4Changelist).not.toHaveBeenCalled();
  });

  it("confirms before unlinking, then asks the server", async () => {
    apiMock.listIssueP4Changelists.mockResolvedValue({ changelists: [changelist({ number: 795115 })] });
    renderSection();
    fireEvent.click(await screen.findByRole("button", { name: "Changelist actions" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Remove from issue" }));
    expect(
      await screen.findByText("Remove CL 795115 from this issue?"),
    ).toBeInTheDocument();
    expect(apiMock.unlinkIssueP4Changelist).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() =>
      expect(apiMock.unlinkIssueP4Changelist).toHaveBeenCalledWith("issue-1", "cl-1"),
    );
  });
});
