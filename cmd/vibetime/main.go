// Command vibetime is the local time-tracking agent and harness hook entrypoint.
package main

import (
	"os"

	"github.com/IzieStratt/vibetime/internal/cli"
)

func main() { os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stderr)) }
