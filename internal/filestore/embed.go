package filestore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/seaweedfs/seaweedfs/weed/command"
)

// StartEmbedded boots an in-process SeaweedFS mini cluster (master + volume +
// filer + S3) on 127.0.0.1 and returns a stop function that tears it down.
// It is used when S3_ENDPOINT is not set, so no external weed binary or
// container is needed.
func StartEmbedded(parent context.Context, dataDir string, cfg Config) (stop func(), err error) {
	mini := findCommand("mini")
	if mini == nil {
		return nil, errors.New("seaweedfs mini command not found")
	}

	// Essential flags. Unknown flags are ignored so the app still boots if a
	// flag was renamed upstream.
	for _, f := range []struct{ name, value string }{
		{"dir", dataDir},
		{"master.port", "9333"},
		{"volume.port", "9340"},
		{"filer.port", "8888"},
		{"s3.port", portOf(cfg.Endpoint)},
		{"webdav", "false"},
		{"admin.ui", "false"},
		{"iceberg", "false"},
		{"lance", "false"},
	} {
		_ = mini.Flag.Set(f.name, f.value) // ignore "no such flag"
	}

	stopPipe, abort, err := routeOutput()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(parent)
	command.MiniClusterCtx = ctx

	done := make(chan struct{})
	go func() {
		mini.Run(mini, nil)
		close(done)
	}()

	// Wait for the S3 gateway to come up, with its own bounded window so the
	// cluster is not tied to (or killed by) the readiness context.
	ready, readyCancel := context.WithTimeout(context.Background(), 120*time.Second)
	err = waitReady(ready, cfg.Endpoint)
	readyCancel()
	if err != nil {
		cancel()
		abort()
		return nil, fmt.Errorf("start embedded seaweedfs: %w", err)
	}

	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		// Leave stdout/stderr redirected: the process is exiting and this keeps
		// SeaweedFS's own shutdown rows from leaking into the real terminal.
		stopPipe()
	}, nil
}

// routeOutput silences the embedded cluster's stdout (the mini banner and
// component status rows) and forwards its stderr (SeaweedFS glog) through the
// app's structured logger. charmbracelet's logger captured os.Stderr at init,
// so our own log lines are unaffected by the swap.
//
// stopPipe stops the stderr reader without restoring the globals; abort
// restores them first (used on the startup-error path, where the app keeps
// running).
func routeOutput() (stopPipe func(), abort func(), err error) {
	oldOut, oldErr := os.Stdout, os.Stderr

	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	os.Stdout = devNull

	pr, pw, err := os.Pipe()
	if err != nil {
		os.Stdout = oldOut
		devNull.Close()
		return nil, nil, err
	}
	os.Stderr = pw

	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			logSeaweedLine(sc.Text())
		}
	}()

	stopPipe = func() {
		pw.Close()
		<-done
	}
	abort = func() {
		os.Stdout = oldOut
		os.Stderr = oldErr
		devNull.Close()
		pw.Close()
		<-done
	}
	return stopPipe, abort, nil
}

// logSeaweedLine wraps one line of SeaweedFS stderr output in the app's
// logger, mapping glog's severity prefix onto the matching level.
func logSeaweedLine(line string) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return
	}
	keyvals := []any{"component", "seaweedfs"}
	switch line[0] {
	case 'W':
		log.Warn(line, keyvals...)
	case 'E', 'F':
		log.Error(line, keyvals...)
	default:
		log.Info(line, keyvals...)
	}
}

// findCommand returns the named top-level command from the seaweedfs registry.
func findCommand(name string) *command.Command {
	for _, c := range command.Commands {
		if c.Name() == name {
			return c
		}
	}
	return nil
}
