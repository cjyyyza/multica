-- P3-B batch 1 rollback: the shared predicate is referenced by
-- UpdateIssue / UpdateIssueStatus, so rolling back the function requires
-- rolling back those query changes in application code as well. The down file
-- only drops the function; the previous migration set (9080) has no dependent
-- schema objects.
DROP FUNCTION IF EXISTS requirement_issue_status_allowed(UUID, UUID, TEXT, TEXT);
