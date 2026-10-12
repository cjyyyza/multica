// @vitest-environment node
import { describe, expect, it } from "vitest";
import { p4DepotIdentity, p4DepotLabel, toP4DepotPayload } from "./p4-depot";

describe("p4DepotIdentity", () => {
  it("lowercases port and ignores user/changelist", () => {
    expect(
      p4DepotIdentity({
        port: "SSL:Perforce.Example.com:1666",
        depot: "//depot/game",
        stream: "//depot/game/main",
      }),
    ).toBe("ssl:perforce.example.com:1666\n//depot/game\n//depot/game/main");
  });
});

describe("p4DepotLabel", () => {
  it("omits an empty stream", () => {
    expect(p4DepotLabel({ port: "p4:1666", depot: "//depot/a" })).toBe(
      "p4:1666 //depot/a",
    );
  });
});

describe("toP4DepotPayload", () => {
  it("drops blank optional fields", () => {
    expect(
      toP4DepotPayload({
        port: "  p4:1666  ",
        depot: "  //depot/a  ",
        stream: "  ",
        description: "  main  ",
        swarm_url: "  https://swarm.example.com/  ",
      }),
    ).toEqual({
      port: "p4:1666",
      depot: "//depot/a",
      description: "main",
      swarm_url: "https://swarm.example.com/",
    });
  });

  // The settings form rebuilds every depot through this function on save, so
  // a switch it drops is a switch an unrelated edit turns off.
  it("keeps the workspace-mode switches and omits them when off", () => {
    expect(
      toP4DepotPayload({
        port: "p4:1666",
        depot: "//depot/a",
        allow_exclusive: true,
        no_auto_review: true,
      }),
    ).toEqual({ port: "p4:1666", depot: "//depot/a", allow_exclusive: true, no_auto_review: true });
    expect(
      toP4DepotPayload({ port: "p4:1666", depot: "//depot/a", allow_exclusive: false, no_auto_review: false }),
    ).toEqual({ port: "p4:1666", depot: "//depot/a" });
  });
});
