export interface RequirementLifecycleIssueFields {
  requirement_id?: string | null;
  origin_type?: string | null;
}

/** Requirement issues use their own lifecycle actions, not generic issue writes. */
export function isRequirementLifecycleIssue(issue: RequirementLifecycleIssueFields): boolean {
  return !!issue.requirement_id || issue.origin_type === "requirement_chat" || issue.origin_type === "requirement_delegation";
}
