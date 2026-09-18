export interface YixiezuoConnection {
  id: string;
  workspace_id: string;
  project_id: string | null;
  cli_bin: string;
  gcp_host: string;
  list_query_id: string;
  external_project_id: string;
  tracker_id: string;
  status_map: Record<string, string>;
  last_pulled_at: string | null;
  last_pushed_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface YixiezuoConnectionEnvelope {
  connection: YixiezuoConnection | null;
  can_manage: boolean;
}

export interface UpsertYixiezuoConnectionRequest {
  project_id?: string | null;
  cli_bin: string;
  gcp_host?: string;
  list_query_id?: string;
  external_project_id?: string;
  tracker_id?: string;
  status_map?: Record<string, string>;
}
