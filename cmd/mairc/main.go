// Command mairc is microagentIRC: a tiny chat system for coding agents, served as an MCP server.
// This file only dispatches subcommands; plan.md §2 and the beads in .beads/ say what each one does.
package main

import (
	"fmt"
	"os"
)

const version = "0.0.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println(version)
	case "daemon", "stdio", "hook", "config", "install", "status":
		fmt.Fprintf(os.Stderr, "mairc %s: not built yet (see the beads in .beads/)\n", os.Args[1])
		os.Exit(1)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mairc daemon|stdio|hook|config|install|status|version")
}
