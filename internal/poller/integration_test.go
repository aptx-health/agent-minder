//go:build integration
// +build integration

package poller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dustinlange/agent-minder/internal/claudecli"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/msgbus"
)

// TestIntegrationAnalysisPipeline tests the full analysis pipeline against
// the Claude Code CLI with the agent-test project. Run with:
//
//	go test -tags integration -run TestIntegrationAnalysisPipeline -v ./internal/poller/
func TestIntegrationAnalysisPipeline(t *testing.T) {
	// Verify claude CLI is available.
	version, err := claudecli.CheckVersion("")
	if err != nil {
		t.Skipf("claude CLI not available: %v", err)
	}
	t.Logf("Claude CLI: %s", version)

	// Open the real minder DB.
	conn, err := db.Open(db.DefaultDBPath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	store := db.NewStore(conn)

	project, err := store.GetProject("agent-test")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}

	t.Logf("Project: %s (summarizer: %s, analyzer: %s)",
		project.Name, project.LLMSummarizerModel, project.LLMAnalyzerModel)

	// Create completer.
	completer := claudecli.NewCLICompleter()

	// Create publisher.
	pub, err := msgbus.NewPublisher(msgbus.DefaultDBPath())
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}
	defer pub.Close()

	// Create poller and do a single poll.
	p := New(store, project, completer, pub)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	result, err := p.doPoll(ctx)
	if err != nil {
		t.Fatalf("doPoll: %v", err)
	}

	fmt.Printf("\n=== POLL RESULT ===\n")
	fmt.Printf("New commits: %d\n", result.NewCommits)
	fmt.Printf("New messages: %d\n", result.NewMessages)
	fmt.Printf("Duration: %s\n", result.Duration)
	fmt.Printf("\n--- Summary ---\n%s\n", result.Tier1Summary)
	fmt.Printf("\n--- Analysis ---\n%s\n", result.Tier2Analysis)
	if result.BusMessageSent != "" {
		fmt.Printf("\n--- Bus Message Sent ---\n%s\n", result.BusMessageSent)
	}
	if len(result.Concerns) > 0 {
		fmt.Printf("\n--- Concerns ---\n")
		for _, c := range result.Concerns {
			fmt.Printf("  %s\n", c)
		}
	}

	// Basic assertions.
	if result.Tier1Summary == "" {
		t.Error("Tier1Summary is empty")
	}
}
