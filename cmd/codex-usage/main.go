package main

import (
	"os"

	"github.com/zJay26/codex-usage/internal/app"
)

func main() {
	os.Exit((app.CLI{Stdout: os.Stdout, Stderr: os.Stderr}).Run(os.Args[1:]))
}
