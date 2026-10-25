package main

import (
	"os"
	"testing"
)

func TestOverviewColorPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, mode, term  string
		terminal, noColor bool
		want              string
	}{
		{name: "default terminal", terminal: true, term: "xterm-256color", want: "always"},
		{name: "auto terminal", mode: "auto", terminal: true, want: "always"},
		{name: "pipe", term: "xterm", want: "never"},
		{name: "NO_COLOR present", terminal: true, noColor: true, want: "never"},
		{name: "dumb terminal", terminal: true, term: "dumb", want: "never"},
		{name: "forced color", mode: "always", noColor: true, term: "dumb", want: "always"},
		{name: "forced plain", mode: "never", terminal: true, want: "never"},
		{name: "invalid option remains invalid", mode: "invalid", terminal: true, want: "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveOverviewColor(tc.mode, tc.terminal, tc.noColor, tc.term); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTerminalDetectionRejectsFilesAndPipes(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	for _, output := range []*os.File{file, null, writer} {
		if isTerminal(output) {
			t.Errorf("%s incorrectly detected as terminal", output.Name())
		}
	}
}
