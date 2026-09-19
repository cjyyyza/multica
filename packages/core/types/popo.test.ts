// @vitest-environment node

import { describe, expect, it } from "vitest";
import { isIdlePopoRobot } from "./popo";

describe("isIdlePopoRobot", () => {
  it("treats a connected robot with empty occupancy as idle", () => {
    expect(isIdlePopoRobot({ connected: true, occupied_by: null })).toBe(true);
    expect(isIdlePopoRobot({ connected: true, occupied_by: "" })).toBe(true);
  });

  it("rejects disconnected robots and those held by another runtime", () => {
    expect(isIdlePopoRobot({ connected: false, occupied_by: null })).toBe(false);
    expect(isIdlePopoRobot({ connected: true, occupied_by: "dj01bot" })).toBe(false);
    expect(isIdlePopoRobot({ connected: true, occupied_by: "sparse" })).toBe(false);
    expect(isIdlePopoRobot({ connected: true, occupied_by: "multica" })).toBe(false);
  });
});
