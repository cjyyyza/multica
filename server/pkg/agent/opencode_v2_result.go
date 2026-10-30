package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

func opencodeV2ConfirmRun(ctx context.Context, conn opencodeRunConnection, streamed eventResult, logger *slog.Logger) (eventResult, error) {
	if streamed.sessionID == "" || len(streamed.messageIDs) == 0 {
		return streamed, errors.New("missing streamed session or message identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{"session", "export", streamed.sessionID}
	if conn.standalone {
		args = append(args, "--standalone")
	} else if conn.server != "" {
		args = append(args, "--server", conn.server)
	}
	cmd := conn.cmd.exec(ctx, args...)
	cmd.Dir, cmd.Env = conn.dir, conn.env
	hideAgentWindow(cmd)
	raw, err := outputOwned(cmd, logger)
	if err != nil {
		return streamed, errors.New("could not read native session result")
	}
	return opencodeV2ResultFromExport(raw, streamed)
}

func opencodeV2ResultFromExport(raw []byte, streamed eventResult) (eventResult, error) {
	var exported struct {
		Info struct {
			ID string `json:"id"`
		} `json:"info"`
		Messages []struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Outcome string `json:"outcome"`
			Finish  string `json:"finish"`
			Time    struct {
				Completed int64 `json:"completed"`
			} `json:"time"`
			Tokens *opencodeTokens `json:"tokens"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &exported); err != nil {
		return streamed, errors.New("invalid native session result")
	}
	if exported.Info.ID != streamed.sessionID || len(exported.Messages) < 2 {
		return streamed, errors.New("native session identity does not match")
	}
	last := exported.Messages[len(exported.Messages)-1]
	if last.Type != "idle" || last.Outcome != "succeeded" {
		return streamed, errors.New("native session has not succeeded")
	}
	seen := make(map[string]bool)
	var usage TokenUsage
	lastAssistant := -1
	for i, message := range exported.Messages {
		if message.Type != "assistant" {
			continue
		}
		lastAssistant = i
		if _, observed := streamed.messageIDs[message.ID]; !observed {
			continue
		}
		if message.Time.Completed <= 0 {
			return streamed, errors.New("streamed assistant message is incomplete")
		}
		seen[message.ID] = true
		if tokens := message.Tokens; tokens != nil {
			usage.InputTokens += tokens.Input
			usage.OutputTokens += tokens.Output
			if tokens.Cache != nil {
				usage.CacheReadTokens += tokens.Cache.Read
				usage.CacheWriteTokens += tokens.Cache.Write
			}
		}
	}
	if lastAssistant < 0 || !seen[exported.Messages[lastAssistant].ID] || exported.Messages[lastAssistant].Finish != "stop" {
		return streamed, errors.New("native terminal message does not complete this run")
	}
	for id := range streamed.messageIDs {
		if !seen[id] {
			return streamed, errors.New("streamed message is missing from native result")
		}
	}
	if usage.InputTokens+usage.OutputTokens+usage.CacheReadTokens+usage.CacheWriteTokens == 0 {
		return streamed, errors.New("native run reported no usage")
	}
	streamed.status, streamed.errMsg = "completed", ""
	streamed.noTerminalSignal, streamed.sawTerminalSignal = false, true
	// Export includes earlier resumed turns; bill only IDs observed in this run.
	streamed.usage = usage
	return streamed, nil
}
