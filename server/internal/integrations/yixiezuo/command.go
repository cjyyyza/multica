package yixiezuo

import (
	"fmt"
	"strings"
)

const CommandHelp = `/yixiezuo preview <issue-url>
/yixiezuo show <operation-id>
/yixiezuo import <issue-url> [--project <project-uuid>]
/yixiezuo import <preview-id> --confirm [--project <project-uuid>]
/yixiezuo refresh <Multica-issue-key>
/yixiezuo publish <Multica-issue-key> [--status <source-status>]
<reviewed result and validation evidence>
/yixiezuo confirm <review-id>`

type Command struct {
	Action, Target, ProjectID, Summary, StatusName string
	Confirmed                                      bool
}

// ParseCommand reads only an explicit first-line directive, never quoted context.
func ParseCommand(text string) (Command, bool, error) {
	header, body, _ := strings.Cut(strings.TrimSpace(text), "\n")
	words := strings.Fields(header)
	if len(words) == 0 || words[0] != "/yixiezuo" {
		return Command{}, false, nil
	}
	command := Command{Action: "help"}
	invalid := func() (Command, bool, error) { return command, true, fmt.Errorf("Use:\n%s", CommandHelp) }
	if len(words) == 1 {
		return command, true, nil
	}
	command.Action = words[1]
	if command.Action == "help" && len(words) == 2 {
		return command, true, nil
	}
	if len(words) < 3 {
		return invalid()
	}
	command.Target = words[2]
	switch command.Action {
	case "preview", "show", "refresh", "confirm":
		if len(words) != 3 || strings.TrimSpace(body) != "" {
			return invalid()
		}
	case "import":
		for i := 3; i < len(words); i++ {
			switch words[i] {
			case "--confirm":
				if command.Confirmed {
					return invalid()
				}
				command.Confirmed = true
			case "--project":
				if command.ProjectID != "" || i+1 >= len(words) {
					return invalid()
				}
				i++
				command.ProjectID = words[i]
			default:
				return invalid()
			}
		}
		if strings.TrimSpace(body) != "" {
			return invalid()
		}
	case "publish":
		if len(words) > 3 {
			if len(words) < 5 || words[3] != "--status" {
				return invalid()
			}
			command.StatusName = strings.Trim(strings.Join(words[4:], " "), "\"")
		}
		command.Summary = strings.TrimSpace(body)
		if command.Summary == "" || len(command.Summary) > 20000 {
			return invalid()
		}
	default:
		return invalid()
	}
	return command, true, nil
}
