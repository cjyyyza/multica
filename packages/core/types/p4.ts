/** Lifecycle of a Perforce changelist linked to an issue. Unknown server
 * values fall back to "pending" in the response schema. */
export type P4ChangelistStatus = "pending" | "shelved" | "submitted" | "deleted";

export interface P4ChangelistWarning {
  /** e.g. "exclusive_held", "not_head", "default_changelist_files". */
  kind: string;
  /** Depot paths the warning refers to. */
  files: string[];
}

export interface P4Changelist {
  id: string;
  workspace_id: string;
  port: string;
  /** Current changelist number; differs from original_number after a
   * submit renumbers the change. */
  number: number;
  original_number: number;
  status: P4ChangelistStatus;
  client: string;
  p4_user: string;
  description: string;
  /** 易协作 ticket the description references as `refs #<ticket>`. */
  ticket: string;
  files_count: number;
  /** +l files this changelist holds open; they block other people. */
  exclusive_files: string[];
  warnings: P4ChangelistWarning[];
  swarm_review_id: string | null;
  swarm_review_number: number | null;
  swarm_review_url: string | null;
  swarm_review_state: string | null;
  task_id: string | null;
  link_source: "task" | "manual";
  submitted_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface IssueP4ChangelistsResponse {
  changelists: P4Changelist[];
}

export interface LinkIssueP4ChangelistRequest {
  /** Changelist number as typed by the user. */
  number: number;
  /** P4PORT; optional when the issue's project has exactly one depot. */
  port?: string;
}
