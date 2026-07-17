package main

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/obot-platform/cmd"

	obotsentry "github.com/obot-platform/obot-sentry/pkg/cmd"
)

func main() {
	cmd.ShutdownSignals = []os.Signal{os.Interrupt}
	root := obotsentry.New()
	if err := root.ExecuteContext(cmd.SetupSignalContext()); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			os.Exit(1)
		}
		// Configuration errors exit 2 so MDM scripts can tell a
		// missing/invalid deployment config from a runtime failure.
		var exitErr *obotsentry.ExitCodeError
		if errors.As(err, &exitErr) {
			log.Print(err)
			os.Exit(exitErr.Code)
		}
		log.Fatal(err)
	}
}
