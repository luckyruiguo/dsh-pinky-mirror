package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brokenWriter is a console that is not there: writing to it fails, as it
// does on Windows when the app runs without a console.
type brokenWriter struct{}

func (brokenWriter) Write(p []byte) (int, error) {
	return 0, errors.New("invalid argument")
}

// The log file is what the person's Send feedback sends. It must get every
// line even when the console is gone.
func TestLogFileSurvivesAMissingConsole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinky.log")
	file, err := openLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	log := slog.New(slog.NewTextHandler(logWriter{file: file, console: brokenWriter{}}, nil))
	log.Info("gateway ready", "socket", "test")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "gateway ready") {
		t.Fatalf("the log file has no line in it: %q", data)
	}
}

// The other way round: when the file cannot be written, the console still
// gets the line, and the error is reported.
func TestLogFileFailureReachesTheConsole(t *testing.T) {
	file, err := openLog(filepath.Join(t.TempDir(), "pinky.log"))
	if err != nil {
		t.Fatal(err)
	}
	file.Close() // writing to it now fails

	var console strings.Builder
	w := logWriter{file: file, console: &console}
	if _, err := w.Write([]byte("a line\n")); err == nil {
		t.Fatal("the closed file's error was swallowed")
	}
	if !strings.Contains(console.String(), "a line") {
		t.Fatalf("the console got nothing: %q", console.String())
	}
}
