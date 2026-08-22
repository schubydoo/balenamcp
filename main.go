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

func main() {
	srv, err := setup(os.Args[1:], os.Stderr)
	if err != nil {
		os.Exit(2) // flag package already printed the parse error + usage
	}
	serve(srv, os.Stderr)
}
