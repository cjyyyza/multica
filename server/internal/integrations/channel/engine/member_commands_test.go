package engine

import (
	"context"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

type fakeMemberCommands struct {
	calls     int
	actor     ResolvedIdentity
	unhandled bool
}

func (f *fakeMemberCommands) HandleMemberCommand(_ context.Context, inst ResolvedInstallation, actor ResolvedIdentity, msg channel.InboundMessage) (Result, bool, error) {
	f.calls++
	f.actor = actor
	return Result{Outcome: OutcomeIssueFollow, InstallationID: inst.ID, ReplyText: "review required"}, !f.unhandled, nil
}

func TestMemberCommandReplayDoesNotEnableIssueCommandsWithoutFollow(t *testing.T) {
	h := newHarness(t)
	set := h.router.sets[channel.TypeFeishu]
	set.Commands = &fakeMemberCommands{unhandled: true}
	h.router.Register(channel.TypeFeishu, set)
	h.router.follow = nil
	h.dedup.claimErr = ErrDuplicate
	msg := p2pMessage(t)
	msg.Text, msg.CommandText = "/issue unrelated command", "/issue unrelated command"
	if err := h.router.Handle(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !h.router.Drain(ctx) || h.binder.ensureCalls != 0 || h.tasks.called {
		t.Fatal("an unrecognized command changed a session or run during replay")
	}
}

func TestMemberCommandsRequireIdentityAndNeverStartChatRuns(t *testing.T) {
	for _, scenario := range []string{"normal", "replay", "unbound", "non-member", "unaddressed-group"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t)
			commands := &fakeMemberCommands{}
			set := h.router.sets[channel.TypeFeishu]
			set.Commands = commands
			h.router.Register(channel.TypeFeishu, set)
			msg := p2pMessage(t)
			msg.Text, msg.CommandText = "/source preview", "/source preview"
			wantCalls := 1
			switch scenario {
			case "replay":
				h.dedup.claimErr = ErrDuplicate
			case "unbound":
				h.ident.err = ErrSenderUnbound
				wantCalls = 0
			case "non-member":
				h.ident.err = ErrSenderNotMember
				wantCalls = 0
			case "unaddressed-group":
				msg.Source.ChatType = channel.ChatTypeGroup
				msg.AddressedToBot = false
				wantCalls = 0
			}
			if err := h.router.Handle(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if !h.router.Drain(ctx) {
				t.Fatal("member command replies did not drain")
			}
			if commands.calls != wantCalls || h.binder.ensureCalls != 0 || h.tasks.called {
				t.Fatalf("calls=%d sessions=%d runs=%v", commands.calls, h.binder.ensureCalls, h.tasks.called)
			}
			if wantCalls == 1 && commands.actor != h.ident.id {
				t.Fatal("member command lost its authenticated actor")
			}
		})
	}
}
