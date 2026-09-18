package yixiezuo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCardListEnvelope(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"success":true,"data":[{"id":12,"subject":"修登录","status":{"name":"开发中"},"priority":{"name":"高"},"updated_on":"2026-01-02T03:04:05Z"}]}`)
	cards, err := parseCardList(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].ExternalID != "12" || cards[0].Title != "修登录" || cards[0].StatusName != "开发中" {
		t.Fatalf("parsed %+v", cards)
	}
}

func TestExecDriverListAndUpdate(t *testing.T) {
	t.Parallel()
	bin := writeFakePMCLI(t)
	drv := NewExecDriver(ExecOptions{
		Bin: bin,
		LookPath: func(file string) (string, error) {
			return file, nil
		},
		Command: exec.CommandContext,
	})
	cards, err := drv.ListCards(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].ExternalID != "7" {
		t.Fatalf("list %+v", cards)
	}
	updated, err := drv.UpdateCard(context.Background(), "7", IssueFields{Title: "新标题", Description: "d"}, "开发中")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "新标题" || updated.StatusName != "开发中" {
		t.Fatalf("update %+v", updated)
	}
}

func writeFakePMCLI(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pm-cli")
	script := `#!/bin/sh
if [ "$1" = "issue" ] && [ "$2" = "filter" ]; then
  printf '%s' '{"success":true,"data":[{"id":7,"subject":"卡片","status":{"name":"新建"},"updated_on":"2026-01-01T00:00:00Z"}]}'
  exit 0
fi
if [ "$1" = "issue" ] && [ "$2" = "update" ]; then
  subject=""
  status=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --subject) subject="$2"; shift ;;
      --status) status="$2"; shift ;;
    esac
    shift
  done
  printf '{"success":true,"data":{"id":7,"subject":"%s","status":{"name":"%s"},"updated_on":"2026-01-03T00:00:00Z"}}' "$subject" "$status"
  exit 0
fi
echo '{"success":false,"error":"unexpected"}' >&2
exit 1
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, "/") {
		t.Fatal("expected absolute fake path")
	}
	return path
}
