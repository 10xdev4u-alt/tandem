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
	"os"

	"github.com/10xdev4u-alt/tandem/internal/config"
	"github.com/10xdev4u-alt/tandem/internal/daemon"
	"github.com/10xdev4u-alt/tandem/internal/version"
)

func main() {
	os.Exit(run())
}

// run returns the exit status rather than calling os.Exit directly, so every
// path through main is a real function that could be called from a test if the
// argument handling ever needs to grow.
func run() int {
	var (
		configPath  = flag.String("config", "", "path to the JSON configuration file")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("tandemd %s (commit %s)\n", version.Version, version.Commit)
		return 0
	}

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "error: --config is required")
		flag.Usage()
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		// The key is named in the error, which is the whole point of
		// validating before startup rather than on first use.
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	d, err := daemon.New(cfg, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	ctx, stop := daemon.SignalContext(context.Background())
	defer stop()

	if err := d.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}
