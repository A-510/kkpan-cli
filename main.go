package main

import (
	"os"

	"github.com/kkpan-cli/kkpan/internal/cliapp"
)

// Version 构建时可通过 -ldflags "-X main.Version=vX.Y.Z" 注入
var Version = "v0.1.0"

func main() {
	if err := cliapp.Run(Version, os.Args); err != nil {
		os.Exit(1)
	}
}
