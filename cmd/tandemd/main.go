// Command tandemd is the Tandem daemon.
//
// Usage:
//
//	tandemd --config path/to/config.json
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/10xdev4u-alt/tandem/internal/config"
	"github.com/10xdev4u-alt/tandem/internal/daemon"
	"github.com/10xdev4u-alt/tandem/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole program, parameterised by its arguments and its two output
// streams.
//
// It takes them as arguments rather than reading os.Args and os.Stderr itself
// for one reason: a main that reaches for the globals cannot be tested, and
// exit statuses are exactly the thing worth testing here. An operator scripting
// this binary needs to know that a missing key is 1 and a missing flag is 2, and
// that promise has to be checkable.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tandemd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath  = fs.String("config", "", "path to the JSON configuration file")
		showVersion = fs.Bool("version", false, "print the version and exit")
	)
	if err := fs.Parse(args); err != nil {
		// ContinueOnError has already written the parse error to stderr.
		return 2
	}

	if *showVersion {
		fmt.Fprintf(stdout, "tandemd %s (commit %s)\n", version.Version, version.Commit)
		return 0
	}

	if *configPath == "" {
		fmt.Fprintln(stderr, "error: --config is required")
		fs.Usage()
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		// The key is named in the error, which is the whole point of
		// validating before startup rather than on first use.
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	d, err := daemon.New(cfg, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	ctx, stop := daemon.SignalContext(context.Background())
	defer stop()

	if err := d.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}
