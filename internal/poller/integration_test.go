// +build integration

package poller

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/llm"
	"github.com/dustinlange/agent-minder/internal/msgbus"
)

// TestIntegrationTwoTierPipeline tests the full two-tier pipeline against
// real LLMs with the agent-test project. Run with:
//
//	go test -tags integration -run TestIntegrationTwoTierPipeline -v ./internal/poller/
func TestIntegrationTwoTierPipeline(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

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

	// Create provider.
	provider, err := llm.NewProvider(project.LLMProvider)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	// Create publisher.
	pub, err := msgbus.NewPublisher(msgbus.DefaultDBPath())
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}
	defer pub.Close()

	// Create poller and do a single poll.
	p := New(store, project, provider, pub)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := p.doPoll(ctx)
	if err != nil {
		t.Fatalf("doPoll: %v", err)
	}

	fmt.Printf("\n=== POLL RESULT ===\n")
	fmt.Printf("New commits: %d\n", result.NewCommits)
	fmt.Printf("New messages: %d\n", result.NewMessages)
	fmt.Printf("Duration: %s\n", result.Duration)
	fmt.Printf("\n--- Tier 1 Summary ---\n%s\n", result.Tier1Summary)
	fmt.Printf("\n--- Tier 2 Analysis ---\n%s\n", result.Tier2Analysis)
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
	// Tier 2 may or may not be empty depending on activity.
	if result.NewMessages > 0 && result.Tier2Analysis == "" {
		t.Error("Tier2Analysis is empty despite new messages")
	}
}
