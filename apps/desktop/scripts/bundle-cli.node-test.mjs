import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const fullRevision = "a".repeat(40);
const scriptDir = fileURLToPath(new URL(".", import.meta.url));

for (const [platform, arch, goos, goarch, foreignCaller] of [
  ["darwin", "arm64", "darwin", "arm64", false],
  ["linux", "x64", "linux", "amd64", false],
  ["win32", "arm64", "windows", "arm64", false],
  ["darwin", "arm64", "darwin", "arm64", true],
  ["linux", "x64", "linux", "amd64", true],
  ["win32", "arm64", "windows", "arm64", true],
]) {
  for (const missingRevision of [false, true]) {
    test(`bundled ${platform}/${arch} preserves source-root commit (missing=${missingRevision}, foreignCaller=${foreignCaller})`, () => {
      const root = mkdtempSync(join(tmpdir(), "multica-bundle-commit-"));
      try {
        const scripts = join(root, "apps", "desktop", "scripts");
        const mockCommands = join(root, "mock-commands.cjs");
        const record = join(root, "build.json");
        const caller = join(root, "foreign-caller");
        mkdirSync(scripts, { recursive: true });
        mkdirSync(join(root, "server"));
        mkdirSync(caller);
        for (const name of ["bundle-cli.mjs", "package.mjs"]) {
          copyFileSync(join(scriptDir, name), join(scripts, name));
        }
        // Preload command doubles in the child so the fixture also works on
        // Windows, where shebang scripts cannot stand in for executables.
        writeFileSync(mockCommands, `
const childProcess = require("node:child_process");
const fs = require("node:fs");
const { syncBuiltinESMExports } = require("node:module");
function git(args, options) {
  let value;
  if (args[0] === "describe") value = "v9.9.9";
  else if (args.join(" ") === "rev-parse --short HEAD") value = "abc1234";
  else if (args.join(" ") === "rev-parse HEAD") {
    if (process.env.TEST_MISSING_REVISION === "true") throw new Error("missing revision");
    value = fs.realpathSync(options.cwd ?? process.cwd()) === process.env.TEST_SOURCE_ROOT ? ${JSON.stringify(fullRevision)} : "b".repeat(40);
  } else throw new Error("unexpected git arguments: " + args.join(" "));
  return options.encoding ? value : Buffer.from(value);
}
childProcess.execFileSync = (command, args, options = {}) => {
  if (command === "git") return git(args, options);
  if (command === "go" && args[0] === "build") {
    fs.writeFileSync(process.env.TEST_BUILD_RECORD, JSON.stringify({args, goos:options.env.GOOS, goarch:options.env.GOARCH, cgo:options.env.CGO_ENABLED}));
    fs.writeFileSync(args[args.indexOf("-o") + 1], "fake artifact");
    return Buffer.alloc(0);
  }
  throw new Error("unexpected executable: " + command);
};
childProcess.execSync = (command, options = {}) => {
  if (command === "go version" || command.startsWith("codesign ")) return Buffer.alloc(0);
  if (command.startsWith("git ")) return git(command.slice(4).split(" "), options);
  throw new Error("unexpected shell command: " + command);
};
syncBuiltinESMExports();
`);
        execFileSync(process.execPath, ["--require", mockCommands, join(scripts, "bundle-cli.mjs"), "--target-platform", platform, "--target-arch", arch], {
          cwd: foreignCaller ? caller : root,
          env: { PATH: "", HOME: root, USERPROFILE: root, TEST_SOURCE_ROOT: realpathSync(root), TEST_BUILD_RECORD: record, TEST_MISSING_REVISION: String(missingRevision) },
          stdio: "pipe",
        });
        const build = JSON.parse(readFileSync(record, "utf8"));
        const flags = build.args[build.args.indexOf("-ldflags") + 1].split(" ");
        assert.ok(flags.includes("main.version=9.9.9"));
        assert.ok(flags.includes("main.commit=abc1234"));
        assert.ok(flags.includes(`main.buildCommit=${missingRevision ? "unknown" : fullRevision}`));
        assert.equal(build.goos, goos);
        assert.equal(build.goarch, goarch);
        assert.equal(build.cgo, "0");
        const artifact = platform === "win32" ? "multica.exe" : "multica";
        assert.equal(readFileSync(join(root, "apps", "desktop", "resources", "bin", artifact), "utf8"), "fake artifact");
      } finally {
        rmSync(root, { recursive: true, force: true });
      }
    });
  }
}
