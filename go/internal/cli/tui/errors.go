package tui

import "errors"

// ErrCancelled is returned when the user cancels an interactive prompt (e.g. Ctrl+C or q).
var ErrCancelled = errors.New("cancelled")

// ErrNotInteractive is returned instead of drawing a prompt when stdin is not a
// terminal — CI, a script, or an AI agent driving the CLI.
//
// The alternative is worse than an error: a Bubble Tea prompt with no terminal
// to read from produces no output and never resolves, so the command appears to
// hang forever with an idle CPU and nothing in its log. That is
// indistinguishable from a slow build, and it cost a full debugging session
// before the cause was found. Failing loudly, naming the flag that fixes it, is
// always better than blocking a caller that can never answer.
var ErrNotInteractive = errors.New("cannot prompt: stdin is not a terminal (pass --yes to accept prompts non-interactively)")
