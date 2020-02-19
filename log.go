package main

import (
	"os"

	"github.com/decred/slog"
)

// logWriter implements an io.Writer that outputs to both standard output and
// the write-end pipe of an initialized log rotator.
type logWriter struct{}

func (logWriter) Write(p []byte) (n int, err error) {
	os.Stdout.Write(p)
	return len(p), nil
}

var (
	backendLog = slog.NewBackend(logWriter{})
	log        = backendLog.Logger("TKBY")
	csppLog    = backendLog.Logger("CSPP")
)

type infoLogger struct{}

var infoLog infoLogger

func (infoLogger) Print(args ...interface{})                 { csppLog.Info(args...) }
func (infoLogger) Printf(format string, args ...interface{}) { csppLog.Infof(format, args...) }
