package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestPingDefault(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"ping"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Count(got, "pong") != 1 {
		t.Fatalf("expected 1 pong, got %q", got)
	}
}

func TestPingCount(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"ping", "--count", "3"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Count(got, "pong") != 3 {
		t.Fatalf("expected 3 pongs, got %q", got)
	}
}
