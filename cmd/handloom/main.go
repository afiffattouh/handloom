// Command handloom is the single handloom binary: hub, link and CLI.
package main

import (
	"os"

	"handloom/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
