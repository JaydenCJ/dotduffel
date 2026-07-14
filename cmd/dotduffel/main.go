// Command dotduffel packs a minimal dotfiles bundle and injects it into
// ssh sessions and containers. All behavior lives in internal/cli so it
// can be tested in-process; main only wires the real process streams
// and exit code.
package main

import (
	"os"

	"github.com/JaydenCJ/dotduffel/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
