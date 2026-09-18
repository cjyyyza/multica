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
  /** Loopback dj01bot webhook base URL. Display only; the API never dials it. */
  webhook_url: string;
  installer_user_id: string;
  status: "active" | "revoked" | string;
  installed_at: string;
  created_at: string;
  updated_at: string;
}

export interface ListPopoInstallationsResponse {
  installations: PopoInstallation[];
  /** Whether the deployment has the at-rest secret key configured. */
  configured: boolean;
  /** Whether the install path is available (true whenever POPO is configured).
   * Optional so an older desktop build that predates it treats it as off. */
  install_supported?: boolean;
}

/** Request body for a robot install. `robot_id` may be empty and becomes `default`. */
export interface RegisterPopoRequest {
  robot_id: string;
  robot_name?: string;
  webhook_url?: string;
  webhook_token?: string;
}

export interface RedeemPopoBindingTokenResponse {
  workspace_id: string;
  installation_id: string;
  popo_user_id: string;
}
