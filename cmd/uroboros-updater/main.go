package main

import (
	"context"
	"errors"
	"github.com/nobanic/UroborOS/stackupdate"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	mode := "serve"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	var err error
	if mode == "snapshot" || mode == "restore" {
		err = stackupdate.VolumeSnapshot(mode == "restore", "/backup", "/volumes")
	} else {
		var config stackupdate.Config
		config, err = stackupdate.ConfigFromEnvironment()
		if err == nil {
			controller := stackupdate.New(config)
			switch mode {
			case "serve":
				err = controller.Serve(ctx)
			case "run":
				err = controller.Run(ctx)
			default:
				err = errors.New("unknown updater command")
			}
		}
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
