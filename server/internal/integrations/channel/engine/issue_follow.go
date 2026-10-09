package engine

import (
	"context"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	replyCommandPrefix  = "/reply"
	statusCommandPrefix = "/status"
	stopCommandPrefix   = "/stop"
)

const (
	InboundWriteKindIssue   = "issue"
	InboundWriteKindComment = "comment"
	InboundWriteKindCancel  = "cancel"
)

// FollowCommandKind is one issue-follow directive on the first non-empty line.
type FollowCommandKind uint8

const (
	FollowCommandReply FollowCommandKind = iota + 1
	FollowCommandStatus
	FollowCommandStop
)

// FollowCommand is a parsed /reply, /status, or /stop directive.
type FollowCommand struct {
	Kind       FollowCommandKind
	Identifier string
	Body       string
}

// ParseFollowCommand classifies /reply, /status, and /stop. Matching follows
// the /issue rules: case-sensitive, token-bounded, first non-empty line only.
func ParseFollowCommand(body string) (FollowCommand, bool) {
	if parsed, ok := parseFollowLeading(body, replyCommandPrefix, true); ok {
		return FollowCommand{Kind: FollowCommandReply, Identifier: parsed.identifier, Body: parsed.body}, true
	}
	if parsed, ok := parseFollowLeading(body, statusCommandPrefix, false); ok {
		return FollowCommand{Kind: FollowCommandStatus, Identifier: parsed.identifier, Body: parsed.body}, true
	}
	if parsed, ok := parseFollowLeading(body, stopCommandPrefix, false); ok {
		return FollowCommand{Kind: FollowCommandStop, Identifier: parsed.identifier, Body: parsed.body}, true
	}
	return FollowCommand{}, false
}

type followParts struct {
	identifier string
	body       string
}

func parseFollowLeading(body, prefix string, keepBody bool) (followParts, bool) {
	rest, ok := parseLeadingCommand(body, prefix)
	if !ok {
		return followParts{}, false
	}
	ident, remaining := splitFollowIdentifier(rest)
	if !keepBody {
		remaining = ""
	}
	return followParts{identifier: ident, body: remaining}, true
}

func splitFollowIdentifier(rest string) (identifier, body string) {
	trimmed := strings.TrimSpace(rest)
	if trimmed == "" {
		return "", ""
	}
	i := strings.IndexFunc(trimmed, unicode.IsSpace)
	if i < 0 {
		return trimmed, ""
	}
	return strings.TrimSpace(trimmed[:i]), strings.TrimSpace(trimmed[i+1:])
}

// MemberCommentInput is the transport-agnostic member comment write used by
// channel follow-ups. Callers pass an explicit actor; nothing here reads HTTP
// headers or forges a user request.
type MemberCommentInput struct {
	WorkspaceID    pgtype.UUID
	IssueID        pgtype.UUID
	ActorUserID    pgtype.UUID
	Content        string
	ParentID       pgtype.UUID
	InstallationID pgtype.UUID
	ChannelType    channel.Type
	MessageID      string
}

// CommentDelivery is how a stored channel comment should be acknowledged.
// It is the result of triggerTasksForComment, not a prediction from the run
// list taken before that call.
type CommentDelivery uint8

const (
	// CommentDeliveryNote keeps the historic comment ack. /note comments never
	// steer and never wait on a later run.
	CommentDeliveryNote CommentDelivery = iota
	// CommentDeliveryInjected means the comment was bound to the single
	// steerable run.
	CommentDeliveryInjected
	// CommentDeliveryWaiting means the comment is a normal follow-up: no
	// steerable run, more than one, or the steer was refused. The comment
	// itself is already stored.
	CommentDeliveryWaiting
)

// MemberCommentResult is the durable comment plus whether this call was a
// replay of an already-persisted inbound write.
type MemberCommentResult struct {
	Comment          db.Comment
	IdempotentReplay bool
	// Delivery is unset on an idempotent replay, which stays silent.
	Delivery CommentDelivery
}

// FollowIssue is the issue snapshot follow commands need.
type FollowIssue struct {
	Issue      db.Issue
	Identifier string
	Prefix     string
	Slug       string
}

// FollowRun is one cancellable (or recently cancelled) issue run.
type FollowRun struct {
	Task   db.AgentTaskQueue
	Status string
}

// CancelRunInput cancels one explicit issue run for an authenticated member.
type CancelRunInput struct {
	WorkspaceID    pgtype.UUID
	ActorUserID    pgtype.UUID
	TaskID         pgtype.UUID
	InstallationID pgtype.UUID
	ChannelType    channel.Type
	MessageID      string
}

// QuoteLookupInput is the validated quote coordinate: robot (installation),
// workspace, chat, and platform message id. Callers must not invent a "latest
// issue" fallback.
type QuoteLookupInput struct {
	InstallationID pgtype.UUID
	WorkspaceID    pgtype.UUID
	ChatID         string
	MessageID      string
}

// QuotedTarget is a channel outbound message that resolved under quote
// validation. Zero IssueID means the quote is not an issue/run follow-up.
type QuotedTarget struct {
	IssueID      pgtype.UUID
	CommentID    pgtype.UUID
	TaskID       pgtype.UUID
	OutboundKind string
	ChatID       string
}

// InboundWrite is one persisted channel issue/comment/cancel result.
type InboundWrite struct {
	Kind      string
	IssueID   pgtype.UUID
	CommentID pgtype.UUID
	TaskID    pgtype.UUID
}

// IssueSourceInput freezes the originating chat for a channel-created issue.
type IssueSourceInput struct {
	WorkspaceID    pgtype.UUID
	IssueID        pgtype.UUID
	InstallationID pgtype.UUID
	ChannelType    channel.Type
	ChatID         string
	ChatType       channel.ChatType
	BindingID      pgtype.UUID
	RouteRevision  int64
}

// IssueFollow is the actor-explicit comment/cancel/status/quote seam. The
// HTTP CreateComment / CancelTaskByUser handlers remain the web entry; this
// interface is what the channel engine calls so it never simulates internal
// HTTP or forges user headers.
type IssueFollow interface {
	CreateMemberComment(ctx context.Context, in MemberCommentInput) (MemberCommentResult, error)
	ResolveIssue(ctx context.Context, workspaceID pgtype.UUID, identifier string) (FollowIssue, error)
	ListActiveRuns(ctx context.Context, issueID pgtype.UUID) ([]FollowRun, error)
	CancelRun(ctx context.Context, in CancelRunInput) (FollowRun, error)
	LookupQuote(ctx context.Context, in QuoteLookupInput) (QuotedTarget, error)
	LoadInboundWrite(ctx context.Context, installationID pgtype.UUID, messageID, kind string) (InboundWrite, error)
	OwnerLabel(ctx context.Context, issue db.Issue) string
}
