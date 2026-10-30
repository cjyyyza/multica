#!/usr/bin/env node
import { readFile } from "node:fs/promises";
import { parseArgs } from "node:util";
import { pathToFileURL } from "node:url";

// The controller deliberately exposes no approval or acceptance commands.
export async function run(argv, env = process.env, request = fetch) {
  const { values, positionals } = parseArgs({ args: argv, allowPositionals: true, options: {
    agent: { type: "string" }, squad: { type: "string" }, ref: { type: "string" },
    "idea-file": { type: "string" }, "content-file": { type: "string" },
    key: { type: "string" }, revision: { type: "string" }, project: { type: "string" },
    limit: { type: "string" }, offset: { type: "string" },
  } });
  const [command, id] = positionals;
  const base = env.MULTICA_URL?.replace(/\/$/, "");
  const workspace = env.MULTICA_WORKSPACE_ID;
  const token = env.MULTICA_TOKEN;
  if (!base || !workspace || !token) throw new Error("Set MULTICA_URL, MULTICA_WORKSPACE_ID and MULTICA_TOKEN");
  const root = `/api/workspaces/${encodeURIComponent(workspace)}/requirements`;
  const detailPath = () => {
    if (!id) throw new Error("A requirement ID is required");
    return `${root}/${encodeURIComponent(id)}`;
  };
  const call = async (route, body, key) => {
    const response = await request(`${base}${route}`, {
      method: body === undefined ? "GET" : "POST",
      headers: { Authorization: `Bearer ${token}`, "X-Workspace-ID": workspace, "X-Client-Capabilities": "requirement-candidate-provenance-v1", "Content-Type": "application/json", ...(key && { "Idempotency-Key": key }) },
      ...(body !== undefined && { body: JSON.stringify(body) }),
      signal: AbortSignal.timeout(30_000),
    });
    const result = await response.json();
    if (!response.ok) {
      // Preserve API codes/revisions and existing_requirement_id for recovery.
      throw new Error(`Multica HTTP ${response.status}: ${JSON.stringify(result)}`);
    }
    return result;
  };
  const key = () => {
    if (!values.key?.trim()) throw new Error("--key is required; reuse it only for an identical retry");
    return values.key;
  };
  const revision = () => {
    const value = Number(values.revision);
    if (!Number.isSafeInteger(value) || value < 1) throw new Error("A positive --revision is required");
    return value;
  };
  const content = async (filename) => {
    if (!filename) throw new Error("A content file is required");
    const text = (await readFile(filename, "utf8")).trim();
    if (!text || Buffer.byteLength(text) > 20_000) throw new Error("Content must contain 1-20000 UTF-8 bytes");
    return text;
  };
  switch (command) {
    case "targets": {
      const [workspaceInfo, agents, squads, runtimes] = await Promise.all([call(`/api/workspaces/${encodeURIComponent(workspace)}`), call("/api/agents"), call("/api/squads"), call("/api/runtimes")]);
      return { workspace: workspaceInfo, agents, squads, runtimes };
    }
    case "plan": {
      if (!values.ref || Boolean(values.agent) === Boolean(values.squad)) throw new Error("Provide --ref and exactly one of --agent or --squad");
      const stableKey = key();
      return call(`/api/workspaces/${encodeURIComponent(workspace)}/requirement-requests`, {
        externalRef: values.ref, idea: await content(values["idea-file"]),
        ...(values.agent ? { agentId: values.agent } : { squadId: values.squad }),
        ...(values.project && { projectId: values.project }),
      }, stableKey);
    }
    case "list": {
      const params = new URLSearchParams();
      if (values.ref) params.set("externalRef", values.ref);
      if (values.limit) params.set("limit", values.limit);
      if (values.offset) params.set("offset", values.offset);
      return call(`${root}?${params}`);
    }
    case "get": return call(detailPath());
    case "reply": {
      const route = detailPath(), stableKey = key(), expectedRevision = revision();
      return call(`${route}/messages`, { expectedRevision, content: await content(values["content-file"]) }, stableKey);
    }
    case "messages": {
      const requirement = await call(detailPath());
      if (!requirement.chatSessionId) return { requirement, messages: [] };
      return { requirement, messages: await call(`/api/chat/sessions/${encodeURIComponent(requirement.chatSessionId)}/messages/page`) };
    }
    case "result": {
      const requirement = await call(detailPath());
      if (!requirement.issueId) return { requirement, issue: null, runs: [], comments: [] };
      const issuePath = `/api/issues/${encodeURIComponent(requirement.issueId)}`;
      const [issue, runs, comments] = await Promise.all([call(issuePath), call(`${issuePath}/task-runs`), call(`${issuePath}/comments`)]);
      return { requirement, issue, runs, comments };
    }
    default: throw new Error("Commands: targets, plan, list, get, messages, reply, result");
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  run(process.argv.slice(2)).then(result => console.log(JSON.stringify(result, null, 2))).catch(error => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
