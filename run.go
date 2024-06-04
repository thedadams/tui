package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/gptscript-ai/go-gptscript"
	"github.com/pterm/pterm"
	"golang.org/x/exp/maps"
)

const ToolCallHeader = "<tool call>"

type RunOptions struct {
	TrustedRepoPrefixes []string
	DisableCache        bool
	Input               string
	CacheDir            string
	SubTool             string
	Workspace           string
}

func first[T comparable](in ...T) (result T) {
	for _, i := range in {
		if i != result {
			return i
		}
	}
	return
}

func complete(opts ...RunOptions) (result RunOptions) {
	for _, opt := range opts {
		result.TrustedRepoPrefixes = append(result.TrustedRepoPrefixes, opt.TrustedRepoPrefixes...)
		result.DisableCache = first(opt.DisableCache, result.DisableCache)
		result.CacheDir = first(opt.CacheDir, result.CacheDir)
		result.SubTool = first(opt.SubTool, result.SubTool)
		result.Workspace = first(opt.Workspace, result.Workspace)
	}
	return
}

func Run(ctx context.Context, tool string, opts ...RunOptions) error {
	var (
		opt   = complete(opts...)
		input = opt.Input
	)

	client, err := gptscript.NewClient()
	if err != nil {
		return err
	}
	defer client.Close()

	confirm, err := NewConfirm(tool, client, opt.TrustedRepoPrefixes...)
	if err != nil {
		return err
	}

	ui, err := newDisplay(tool)
	if err != nil {
		return err
	}
	defer ui.Close()

	run, err := client.Run(ctx, tool, gptscript.Options{
		Confirm:       true,
		IncludeEvents: true,
		DisableCache:  opt.DisableCache,
		Input:         opt.Input,
		CacheDir:      opt.CacheDir,
		SubTool:       opt.SubTool,
		Workspace:     opt.Workspace,
	})
	if err != nil {
		return err
	}
	defer run.Close()

	for {
		var text string

		for event := range run.Events() {
			text = render(input, run)
			if err := ui.Progress(text); err != nil {
				return err
			}

			if ok, err := confirm.HandlePrompt(ctx, event, ui.Ask); !ok || err != nil {
				return err
			}

			if ok, err := confirm.HandleConfirm(ctx, event, ui.AskYesNo); !ok || err != nil {
				return err
			}
		}

		err = ui.Finished(text)
		if err != nil {
			return err
		}

		if run.State().IsTerminal() {
			return run.Err()
		}

		line, ok, err := ui.Prompt(getCurrentToolName(run))
		if err != nil || !ok {
			return err
		}

		input = line
		run, err = run.NextChat(ctx, input)
		if err != nil {
			return err
		}
	}
}

func render(input string, run *gptscript.Run) string {
	buf := &strings.Builder{}

	if input != "" {
		buf.WriteString(color.GreenString("> "+input) + "\n")
	}

	if call, ok := run.ParentCallFrame(); ok {
		printCall(buf, run.Calls(), call)
	}

	return buf.String()
}

func printToolCall(buf *strings.Builder, toolCall string) {
	// The intention here is to only print the string while it's still being generated, if it's complete
	// then there's no reason to because we are waiting on something else at that point and it's status should
	// be displayed
	lines := strings.Split(toolCall, "\n")
	line := lines[len(lines)-1]

	data := map[string]any{}
	name, args, _ := strings.Cut(strings.TrimPrefix(line, ToolCallHeader), " -> ")
	if err := json.Unmarshal([]byte(args), &data); err == nil {
		return
	}

	width := pterm.GetTerminalWidth() - 33
	if len(args) > width {
		args = fmt.Sprintf("%s %s...(%d)", name, args[:width], len(args[width:]))
	} else {
		args = fmt.Sprintf("%s %s", name, args)
	}

	buf.WriteString("Preparing call:")
	buf.WriteString(args)
	buf.WriteString("\n")
}

func printCall(buf *strings.Builder, calls map[string]gptscript.CallFrame, call gptscript.CallFrame) {
	if call.DisplayText != "" {
		s, err := MarkdownRender.Render(call.DisplayText)
		if err == nil {
			buf.WriteString(s)
		}
	}

	for _, output := range call.Output {
		content, toolCall, _ := strings.Cut(output.Content, ToolCallHeader)
		if content != "" {
			if strings.HasPrefix(call.Tool.Instructions, "#!") {
				buf.WriteString(BoxStyle.Render(strings.TrimSpace(content)))
			} else {
				s, err := MarkdownRender.Render(content)
				if err == nil {
					buf.WriteString(s)
				} else {
					buf.WriteString(content)
				}
			}
		}

		if toolCall != "" {
			printToolCall(buf, ToolCallHeader+toolCall)
		}

		keys := maps.Keys(output.SubCalls)
		sort.Slice(keys, func(i, j int) bool {
			return calls[keys[i]].Start.Before(calls[keys[j]].Start)
		})

		for _, key := range keys {
			printCall(buf, calls, calls[key])
		}
	}
}

func getCurrentToolName(run *gptscript.Run) string {
	toolName := run.RespondingTool().Name
	if toolName == "" {
		return toolName
	}
	return "@" + toolName
}