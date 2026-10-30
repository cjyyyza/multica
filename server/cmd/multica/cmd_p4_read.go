package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func p4ReadCommands() []*cobra.Command {
	changes := newP4ReadCmd("changes", "List changelists and print p4's output",
		"Runs `p4 changes` in the bound client and prints p4's own output. "+
			"Flags after the verb are forwarded (`-m`, `-s`, `-l`, `-L`, `-e`, `-u`, filespecs). "+
			"With no filespec, the list is limited to this task's authorized depots. `-m` defaults to 50 and cannot exceed 200.")
	changes.Aliases = []string{"changelists"}
	return []*cobra.Command{
		changes,
		newP4ReadCmd("fstat", "Show file state in the bound client and print p4's output",
			"Runs `p4 fstat` in the bound client and prints p4's own output, including haveRev, action, and otherOpen. "+
				"Flags after the verb are forwarded (`-Ol`, `-F`, `-T`, `-e`, `-c`, `-m`). "+
				"A wildcard must name a subdirectory and include `-m` (at most 200). A whole-depot wildcard is rejected. Raw `p4 fstat` cannot see this client."),
		newP4ReadCmd("files", "List depot files and print p4's output",
			"Runs `p4 files` in the bound client and prints p4's own output. Individual files, or a subdirectory wildcard with `-m`."),
		newP4ReadCmd("dirs", "List depot subdirectories and print p4's output",
			"Runs `p4 dirs` on directories in an authorized depot and prints p4's own output. Whole-depot wildcards are rejected."),
		newP4ReadCmd("grep", "Search depot files and print p4's output",
			"Runs `p4 grep` on files in an authorized depot and prints p4's own output. Whole-depot wildcards are rejected."),
		newP4ReadCmd("print", "Print file contents from the depot",
			"Runs `p4 print` on individual files in an authorized depot and prints p4's own output. Wildcards and `-o` are rejected."),
		newP4ReadCmd("diff", "Diff local files against the depot and print p4's output",
			"Runs `p4 diff` on individual files in the bound client and prints p4's own output. Wildcards are rejected."),
		newP4ReadCmd("diff2", "Diff two depot revisions and print p4's output",
			"Runs `p4 diff2` on individual files in an authorized depot and prints p4's own output. Wildcards are rejected."),
		newP4ReadCmd("annotate", "Annotate a file and print p4's output",
			"Runs `p4 annotate` on individual files in an authorized depot and prints p4's own output. Wildcards are rejected."),
		newP4ReadCmd("where", "Show where a file is mapped and print p4's output",
			"Runs `p4 where` in the bound client and prints p4's own output. Wildcards are rejected."),
		newP4ReadCmd("have", "Show the have revision and print p4's output",
			"Runs `p4 have` in the bound client and prints p4's own output. Wildcards are rejected."),
		newP4ReadCmd("sizes", "Show depot file sizes and print p4's output",
			"Runs `p4 sizes` on individual files in an authorized depot and prints p4's own output. Wildcards are rejected."),
		newP4ReadCmd("integrated", "List submitted integrations and print p4's output",
			"Runs `p4 integrated` on files in authorized depots and prints p4's own output. Whole-depot wildcards are rejected."),
		newP4ReadCmd("interchanges", "List changes awaiting integration and print p4's output",
			"Runs `p4 interchanges` on files in authorized depots and prints p4's own output. Whole-depot wildcards are rejected."),
		newP4ReadCmd("resolved", "List resolved files and print p4's output",
			"Runs `p4 resolved` on individual files in authorized depots and prints p4's own output. Wildcards are rejected."),
	}
}

func newP4ReadCmd(verb, short, long string) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " [p4-args...]",
		Short: short,
		Long: long + " `--output json` prints the daemon envelope; the default is p4's text. " +
			"`-h` shows this help. Do not pass `--port` or `--depot`.",
		DisableFlagParsing: true,
		RunE:               runP4Read(verb),
	}
}

func runP4Read(action string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		for _, arg := range args {
			if arg == "-h" || arg == "--help" {
				return cmd.Help()
			}
		}
		output, rest, err := splitP4ReadCLIArgs(args)
		if err != nil {
			return err
		}
		ref, err := optionalP4PortDepot()
		if err != nil {
			return err
		}
		body, err := p4TaskEnv(ref)
		if err != nil {
			return err
		}
		body["action"] = action
		if len(rest) > 0 {
			body["args"] = rest
		}
		payload, err := p4DaemonPost(cmd.Context(), "/p4/run", body, p4DaemonTimeout())
		if err != nil {
			return reportP4Failure("p4 "+action, err)
		}
		var result struct {
			Output   string `json:"output"`
			Stderr   string `json:"stderr"`
			ExitCode int    `json:"exit_code"`
		}
		if err := json.Unmarshal(payload, &result); err != nil {
			return fmt.Errorf("parse response: %w", err)
		}
		if output == "json" {
			var parsed any
			if err := json.Unmarshal(payload, &parsed); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}
			if err := cli.PrintJSON(os.Stdout, parsed); err != nil {
				return err
			}
		} else if result.Output != "" {
			fmt.Fprintln(os.Stdout, result.Output)
		}
		if result.ExitCode != 0 {
			if result.Stderr != "" {
				fmt.Fprintln(os.Stderr, result.Stderr)
			}
			return fmt.Errorf("p4 %s exited %d", action, result.ExitCode)
		}
		return nil
	}
}

// splitP4ReadCLIArgs strips Multica-only long flags. p4 flags are single-dash
// and stay in the forwarded argv, including `-F` which is a p4 filter here
// rather than the opened-file formatter.
func splitP4ReadCLIArgs(args []string) (string, []string, error) {
	output := "text"
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, attached := strings.Cut(arg, "=")
		take := func() (string, error) {
			if attached {
				return value, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s requires a value", name)
			}
			i++
			return args[i], nil
		}
		switch name {
		case "--":
			continue
		case "--output":
			got, err := take()
			if err != nil {
				return "", nil, err
			}
			output = got
		case "--port":
			got, err := take()
			if err != nil {
				return "", nil, err
			}
			p4Port = got
		case "--depot":
			got, err := take()
			if err != nil {
				return "", nil, err
			}
			p4DepotPath = got
		case "--user":
			got, err := take()
			if err != nil {
				return "", nil, err
			}
			p4User = got
		case "--charset":
			got, err := take()
			if err != nil {
				return "", nil, err
			}
			p4Charset = got
		default:
			rest = append(rest, arg)
		}
	}
	if output != "text" && output != "json" {
		return "", nil, fmt.Errorf("output must be text or json")
	}
	return output, rest, nil
}
