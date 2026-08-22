package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/schubydoo/balenamcp/server"
)

// setup parses argv, applies flag state to the server config, and returns the
// wired MCP server. Split from main so the flag-parse → config → SetupServer
// path is testable in-process.
func setup(args []string, stderr io.Writer) (*mcpserver.MCPServer, error) {
	fs := flag.NewFlagSet("balenamcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "Enable dry-run mode")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	server.Config.DryRun = *dryRun
	return server.SetupServer(), nil
}

// serve runs the stdio loop until the client closes stdin. Split from main so
// the banner and the error-reporting branch are testable by swapping
// os.Stdin (which ServeStdio reads directly) for a pipe.
func serve(srv *mcpserver.MCPServer, stderr io.Writer) {
	fmt.Fprintf(stderr, "Starting BalenaMCP server...\n")
	if err := mcpserver.ServeStdio(srv); err != nil {
		fmt.Fprintf(stderr, "Server error: %v\n", err)
	}
}

// run is main's body with its process-globals injected: argv, stderr, and
// the exit function. Injecting exit is what makes the flag-error path
// testable — os.Exit cannot run under go test, but a recorder can.
func run(args []string, stderr io.Writer, exit func(int)) {
	srv, err := setup(args, stderr)
	if err != nil {
		exit(2) // flag package already printed the parse error + usage
		return
	}
	serve(srv, stderr)
}

func main() {
	run(os.Args[1:], os.Stderr, os.Exit)
}
