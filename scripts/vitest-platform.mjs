// Bound Windows worker counts to keep concurrent workspace suites responsive.
// Let jsdom own browser storage instead of Node's unrelated Web Storage globals.
export const platformTestOptions = {
  maxWorkers: process.platform === "win32" ? 2 : undefined,
  execArgv: process.allowedNodeEnvironmentFlags.has("--no-experimental-webstorage")
    ? ["--no-experimental-webstorage"]
    : [],
};
