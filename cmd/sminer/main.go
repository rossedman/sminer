// Command sminer mines Claude Code session transcripts for corrections you
// keep repeating and proposes hooks, CLAUDE.md lines or skills that fix them.
package main

import (
	"os"

	"github.com/rossedman/sminer/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
