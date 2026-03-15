package msgbus

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// setupTestDB creates an in-memory SQLite DB with agent-msg schema and test data.
func setupTestDB(t *testing.T) *Client {
	t.Helper()

	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	schema := `
		CREATE TABLE messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			topic TEXT NOT NULL,
			sender TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at TEXT DEFAULT (datetime('now'))
		);
		CREATE INDEX idx_topic ON messages(topic);
		CREATE INDEX idx_created_at ON messages(created_at);

		CREATE TABLE acks (
			message_id INTEGER NOT NULL,
			agent_name TEXT NOT NULL,
			acked_at TEXT DEFAULT (datetime('now')),
			PRIMARY KEY (message_id, agent_name)
		);
		CREATE INDEX idx_acks_agent ON acks(agent_name);

		CREATE TABLE agent_names (
			repo TEXT NOT NULL,
			name TEXT NOT NULL,
			claimed_at TEXT DEFAULT (datetime('now')),
			PRIMARY KEY (repo, name)
		);
		CREATE INDEX idx_agent_names_repo ON agent_names(repo, claimed_at);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}

	// Insert test messages.
	_, _ = db.Exec(`INSERT INTO messages (topic, sender, message) VALUES ('ripit/app', 'ripit/Cornelius', 'Added auth middleware')`)
	_, _ = db.Exec(`INSERT INTO messages (topic, sender, message) VALUES ('ripit/app', 'ripit/Bartholomew', 'Fixed login bug')`)
	_, _ = db.Exec(`INSERT INTO messages (topic, sender, message) VALUES ('ripit/infra', 'ripit/Tobias', 'k3s cluster ready')`)
	_, _ = db.Exec(`INSERT INTO messages (topic, sender, message) VALUES ('other/topic', 'other/Agent', 'Unrelated message')`)

	// Ack message 1 for one agent.
	_, _ = db.Exec(`INSERT INTO acks (message_id, agent_name) VALUES (1, 'ripit/minder')`)

	// Register an agent name.
	_, _ = db.Exec(`INSERT INTO agent_names (repo, name) VALUES ('ripit', 'Cornelius')`)

	return &Client{db: db}
}

func TestRecentMessages(t *testing.T) {
	c := setupTestDB(t)
	defer func() { _ = c.Close() }()

	msgs, err := c.RecentMessages(1*time.Hour, "")
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Errorf("got %d messages, want 4", len(msgs))
	}
}

func TestRecentMessagesFiltered(t *testing.T) {
	c := setupTestDB(t)
	defer func() { _ = c.Close() }()

	msgs, err := c.RecentMessages(1*time.Hour, "ripit")
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("got %d messages, want 3 (ripit/* only)", len(msgs))
	}
}

func TestUnreadMessages(t *testing.T) {
	c := setupTestDB(t)
	defer func() { _ = c.Close() }()

	// ripit/minder has acked message 1, so should see 3 unread.
	msgs, err := c.UnreadMessages("ripit/minder", "")
	if err != nil {
		t.Fatalf("UnreadMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("got %d unread, want 3", len(msgs))
	}
}

func TestUnreadMessagesFiltered(t *testing.T) {
	c := setupTestDB(t)
	defer func() { _ = c.Close() }()

	// ripit/minder acked msg 1 (ripit/app). Remaining ripit/* unread: msg 2 (ripit/app) + msg 3 (ripit/infra).
	msgs, err := c.UnreadMessages("ripit/minder", "ripit")
	if err != nil {
		t.Fatalf("UnreadMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("got %d unread, want 2", len(msgs))
	}
}

func TestTopicsSummary(t *testing.T) {
	c := setupTestDB(t)
	defer func() { _ = c.Close() }()

	summaries, err := c.TopicsSummary(1*time.Hour, "ripit", "ripit/minder")
	if err != nil {
		t.Fatalf("TopicsSummary: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("got %d topics, want 2", len(summaries))
	}

	// Check that unread counts are present.
	for _, s := range summaries {
		if s.Topic == "ripit/app" {
			if s.MessageCount != 2 {
				t.Errorf("ripit/app msg_count = %d, want 2", s.MessageCount)
			}
			if s.UnreadCount != 1 {
				t.Errorf("ripit/app unread = %d, want 1", s.UnreadCount)
			}
		}
	}
}

func TestMessageCount(t *testing.T) {
	c := setupTestDB(t)
	defer func() { _ = c.Close() }()

	count, err := c.MessageCount("")
	if err != nil {
		t.Fatalf("MessageCount: %v", err)
	}
	if count != 4 {
		t.Errorf("count = %d, want 4", count)
	}

	count, err = c.MessageCount("ripit")
	if err != nil {
		t.Fatalf("MessageCount: %v", err)
	}
	if count != 3 {
		t.Errorf("filtered count = %d, want 3", count)
	}
}

func TestActiveAgents(t *testing.T) {
	c := setupTestDB(t)
	defer func() { _ = c.Close() }()

	agents, err := c.ActiveAgents(1*time.Hour, "ripit")
	if err != nil {
		t.Fatalf("ActiveAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}
	if agents[0] != "ripit/Cornelius" {
		t.Errorf("agent = %q, want %q", agents[0], "ripit/Cornelius")
	}
}

func TestPublisher(t *testing.T) {
	// Create a file-based DB for the publisher (can't use :memory: across connections).
	dbPath := filepath.Join(t.TempDir(), "pub_test.db")
	db, err := sqlx.Open("sqlite", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	schema := `
		CREATE TABLE messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			topic TEXT NOT NULL,
			sender TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at TEXT DEFAULT (datetime('now'))
		);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	_ = db.Close()

	// Test the publisher.
	pub, err := NewPublisher(dbPath)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	defer func() { _ = pub.Close() }()

	if err := pub.Publish("test/coord", "test/minder", "Hello from publisher"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Verify via read-only client.
	client, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	msgs, err := client.RecentMessages(1*time.Hour, "test")
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Topic != "test/coord" {
		t.Errorf("topic = %q, want %q", msgs[0].Topic, "test/coord")
	}
	if msgs[0].Sender != "test/minder" {
		t.Errorf("sender = %q, want %q", msgs[0].Sender, "test/minder")
	}
	if msgs[0].Message != "Hello from publisher" {
		t.Errorf("message = %q, want %q", msgs[0].Message, "Hello from publisher")
	}
}

func TestOpenEmptyDB(t *testing.T) {
	// Opening a path that auto-creates an empty DB should fail
	// when we try to query (no tables), but Open itself succeeds
	// because SQLite creates the file on connect.
	dbPath := filepath.Join(t.TempDir(), "empty.db")
	c, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Querying should fail since there are no tables.
	_, err = c.MessageCount("")
	if err == nil {
		t.Error("expected error querying empty DB with no tables")
	}
}
