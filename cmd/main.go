package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

// Build information, injected at link time:
//
//	-ldflags "-X main.version=v1.2.3 -X main.commit=abc1234 -X main.buildDate=..."
//
// It is what ties a running container back to a commit; without it, "which build
// is this" has no answer once an image has been retagged.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	// A subcommand rather than a flag, so `docker run <image> version` works
	// without overriding the entrypoint. Checked before flag.Parse because the
	// flag package treats a bare word as a positional argument and would carry on
	// into a normal startup.
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("cryptopay %s (commit %s, built %s, %s %s/%s)\n",
			version, commit, buildDate, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return
	}

	configPath := flag.String("config", "config.json",
		"path to the JSON configuration file")
	checkConfig := flag.Bool("check-config", false,
		"load the configuration, report any problems, and exit")
	flag.Parse()

	if *checkConfig {
		os.Exit(checkConfigFile(config.Path(*configPath)))
	}

	app, err := InitializeApp(config.Path(*configPath))
	if err != nil {
		log.Fatalf("init app: %v", err)
	}
	app.Run(BuildInfo{Version: version, Commit: commit, Date: buildDate})
}
