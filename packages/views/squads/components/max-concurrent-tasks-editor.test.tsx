import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enSquads from "../../locales/en/squads.json";
import { MaxConcurrentTasksEditor } from "./max-concurrent-tasks-editor";

describe("MaxConcurrentTasksEditor", () => {
  it.each([
    { failure: new Error("Squad permission changed"), message: "Squad permission changed" },
    { failure: null, message: "Failed to save" },
  ])("shows save failures and preserves the draft for retry ($message)", async ({ failure, message }) => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockRejectedValueOnce(failure).mockResolvedValueOnce(undefined);
    render(
      <I18nProvider locale="en" resources={{ en: { squads: enSquads } }}>
        <MaxConcurrentTasksEditor value={3} onSave={onSave} />
      </I18nProvider>,
    );

    await user.click(screen.getByRole("button", { name: "3" }));
    fireEvent.change(screen.getByRole("spinbutton"), { target: { value: "4" } });
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(message);
    expect(screen.getByRole("spinbutton")).toHaveValue(4);
    expect(onSave).toHaveBeenLastCalledWith(4);

    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument());
    expect(onSave).toHaveBeenCalledTimes(2);
    expect(onSave).toHaveBeenLastCalledWith(4);
  });
});
