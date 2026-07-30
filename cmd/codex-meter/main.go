package main

import (
	"os"

	"github.com/local-first/codex-meter/internal/app"
)

func main() {
	os.Exit((app.CLI{Stdout: os.Stdout, Stderr: os.Stderr}).Run(os.Args[1:]))
}
