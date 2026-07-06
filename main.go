package main

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/obot-platform/cmd"

	obocop "github.com/obot-platform/obocop/pkg/cmd"
)

func main() {
	cmd.ShutdownSignals = []os.Signal{os.Interrupt}
	root := obocop.New()
	if err := root.ExecuteContext(cmd.SetupSignalContext()); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			os.Exit(1)
		}
		// Configuration errors exit 2 so MDM scripts can tell a
		// missing/invalid deployment config from a runtime failure.
		var exitErr *obocop.ExitCodeError
		if errors.As(err, &exitErr) {
			log.Print(err)
			os.Exit(exitErr.Code)
		}
		log.Fatal(err)
	}
}
