/** A POPO Open / dj01bot installation bound to a single Multica agent.
 *
 * Wire shape mirrors `PopoInstallationResponse` in
 * `server/internal/handler/popo.go`. New fields the backend adds in the
 * future MUST default to optional so older desktop builds keep parsing the
 * response — see CLAUDE.md → API Compatibility. */
export interface PopoInstallation {
  id: string;
  workspace_id: string;
  agent_id: string;
  /** dj01bot robot id (`websocketRobots[].id`, or `default` for the webhook robot). */
  robot_id: string;
  robot_name: string;
  /** Windows bridge this robot is bound through. Empty on older rows. */
  bridge_id: string;
  /** Loopback dj01bot webhook base URL. Display only; the API never dials it.
   * New installs return `""`; kept so older desktop builds keep parsing. */
  webhook_url: string;
  installer_user_id: string;
  status: "active" | "revoked" | string;
  installed_at: string;
  created_at: string;
  updated_at: string;
}

export interface ListPopoInstallationsResponse {
  installations: PopoInstallation[];
  /** Whether this deployment has POPO enabled (`MULTICA_POPO_ENABLED=true`). */
  configured: boolean;
  /** Whether the install path is available (true whenever POPO is configured).
   * Optional so an older desktop build that predates it treats it as off. */
  install_supported?: boolean;
}

/** Request body for binding an idle robot on a paired Windows bridge. */
export interface RegisterPopoRequest {
  bridge_id: string;
  robot_id: string;
  robot_name?: string;
}

export interface RedeemPopoBindingTokenResponse {
  workspace_id: string;
  installation_id: string;
  popo_user_id: string;
}

/** Heartbeat occupancy: `null`/empty is idle; otherwise dj01bot, sparse, or multica. */
export type PopoRobotOccupant = "dj01bot" | "sparse" | "multica" | string;

/** One robot reported on a Windows bridge heartbeat.
 * Mirrors `popo.RobotReport` in `server/internal/integrations/popo/bridge.go`. */
export interface PopoBridgeRobot {
  robot_id: string;
  display_name: string;
  connected: boolean;
  occupied_by: PopoRobotOccupant | null;
}

/** Workspace-visible Windows bridge.
 * Mirrors `PopoBridgeResponse` in `server/internal/handler/popo_bridge.go`. */
export interface PopoBridge {
  id: string;
  hostname: string;
  status: "active" | "revoked" | string;
  online: boolean;
  last_heartbeat_at: string;
  robots: PopoBridgeRobot[];
  created_at: string;
}

export interface ListPopoBridgesResponse {
  bridges: PopoBridge[];
  configured: boolean;
}

/** Pairing-code mint. `pairing_code` is returned once and must not be cached. */
export interface PopoBridgePairing {
  id: string;
  pairing_code: string;
  expires_at: string;
  ttl_seconds: number;
}

export interface CreatePopoBridgePairingRequest {
  hostname?: string;
}

/** Bindable = connected and not held by the ordinary gateway or Sparse.
 * `occupied_by=multica` means this Windows bridge holds the websocket, which
 * is the first-bind state — not an agent occupancy. */
export function isIdlePopoRobot(robot: Pick<PopoBridgeRobot, "connected" | "occupied_by">): boolean {
  if (robot.connected !== true) return false;
  const occupied = robot.occupied_by;
  return !occupied || occupied === "multica";
}
