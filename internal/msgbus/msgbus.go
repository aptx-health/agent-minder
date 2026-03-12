package msgbus

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/dustinlange/agent-minder/internal/sqliteutil"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// Message represents a row from the messages table.
type Message struct {
	ID        int64  `db:"id"`
	Topic     string `db:"topic"`
	Sender    string `db:"sender"`
	Message   string `db:"message"`
	CreatedAt string `db:"created_at"`
}

// TopicSummary represents aggregated info about a topic.
type TopicSummary struct {
	Topic        string `db:"topic"`
	MessageCount int    `db:"msg_count"`
	Senders      string `db:"senders"`
	LastActivity string `db:"last_activity"`
	UnreadCount  int    `db:"unread_count"`
}

// Client provides read access to the agent-msg SQLite database.
type Client struct {
	db *sqlx.DB
}

// DefaultDBPath returns the agent-msg DB path, respecting AGENT_MSG_DB env var.
func DefaultDBPath() string {
	if p := os.Getenv("AGENT_MSG_DB"); p != "" {
		return p
	}
	// Fall back to the standard location.
	home, _ := os.UserHomeDir()
	if home != "" {
		return home + "/repos/agent-msg/messages.db"
	}
	return "messages.db"
}

// Open connects to the agent-msg SQLite database (read-only) with automatic
// WAL recovery if stale -shm/-wal files are detected.
func Open(dbPath string) (*Client, error) {
	db, err := sqliteutil.OpenWithRecovery(dbPath, dbPath+"?mode=ro&_journal_mode=WAL&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening agent-msg DB: %w", err)
	}
	return &Client{db: db}, nil
}

// Close closes the database connection.
func (c *Client) Close() error {
	return c.db.Close()
}

// RecentMessages returns messages from the last `since` duration,
// optionally filtered by topic prefix (e.g., "ripit" matches "ripit/*").
func (c *Client) RecentMessages(since time.Duration, topicPrefix string) ([]Message, error) {
	cutoff := time.Now().UTC().Add(-since).Format("2006-01-02 15:04:05")

	var msgs []Message
	var err error

	if topicPrefix == "" {
		err = c.db.Select(&msgs,
			`SELECT id, topic, sender, message, created_at
			 FROM messages
			 WHERE created_at >= ?
			 ORDER BY created_at DESC`, cutoff)
	} else {
		err = c.db.Select(&msgs,
			`SELECT id, topic, sender, message, created_at
			 FROM messages
			 WHERE created_at >= ? AND topic LIKE ?
			 ORDER BY created_at DESC`, cutoff, topicPrefix+"/%")
	}

	if err != nil {
		return nil, fmt.Errorf("querying messages: %w", err)
	}
	return msgs, nil
}

// UnreadMessages returns messages not yet acked by the given agent identity.
func (c *Client) UnreadMessages(agentName string, topicPrefix string) ([]Message, error) {
	var msgs []Message
	var err error

	if topicPrefix == "" {
		err = c.db.Select(&msgs,
			`SELECT m.id, m.topic, m.sender, m.message, m.created_at
			 FROM messages m
			 LEFT JOIN acks a ON m.id = a.message_id AND a.agent_name = ?
			 WHERE a.message_id IS NULL
			 ORDER BY m.created_at DESC`, agentName)
	} else {
		err = c.db.Select(&msgs,
			`SELECT m.id, m.topic, m.sender, m.message, m.created_at
			 FROM messages m
			 LEFT JOIN acks a ON m.id = a.message_id AND a.agent_name = ?
			 WHERE a.message_id IS NULL AND m.topic LIKE ?
			 ORDER BY m.created_at DESC`, agentName, topicPrefix+"/%")
	}

	if err != nil {
		return nil, fmt.Errorf("querying unread messages: %w", err)
	}
	return msgs, nil
}

// TopicsSummary returns summary info for active topics within the given window.
func (c *Client) TopicsSummary(since time.Duration, topicPrefix string, agentName string) ([]TopicSummary, error) {
	cutoff := time.Now().UTC().Add(-since).Format("2006-01-02 15:04:05")

	var query string
	var args []interface{}

	if agentName != "" {
		query = `SELECT
			m.topic,
			COUNT(*) as msg_count,
			GROUP_CONCAT(DISTINCT m.sender) as senders,
			MAX(m.created_at) as last_activity,
			SUM(CASE WHEN a.message_id IS NULL THEN 1 ELSE 0 END) as unread_count
		FROM messages m
		LEFT JOIN acks a ON m.id = a.message_id AND a.agent_name = ?
		WHERE m.created_at >= ?`
		args = append(args, agentName, cutoff)
	} else {
		query = `SELECT
			m.topic,
			COUNT(*) as msg_count,
			GROUP_CONCAT(DISTINCT m.sender) as senders,
			MAX(m.created_at) as last_activity,
			0 as unread_count
		FROM messages m
		WHERE m.created_at >= ?`
		args = append(args, cutoff)
	}

	if topicPrefix != "" {
		query += ` AND m.topic LIKE ?`
		args = append(args, topicPrefix+"/%")
	}

	query += ` GROUP BY m.topic ORDER BY last_activity DESC`

	var summaries []TopicSummary
	if err := c.db.Select(&summaries, query, args...); err != nil {
		return nil, fmt.Errorf("querying topic summaries: %w", err)
	}
	return summaries, nil
}

// MessageCount returns the total number of messages, optionally filtered by topic prefix.
func (c *Client) MessageCount(topicPrefix string) (int, error) {
	var count int
	var err error

	if topicPrefix == "" {
		err = c.db.Get(&count, `SELECT COUNT(*) FROM messages`)
	} else {
		err = c.db.Get(&count, `SELECT COUNT(*) FROM messages WHERE topic LIKE ?`, topicPrefix+"/%")
	}

	if err != nil {
		return 0, fmt.Errorf("counting messages: %w", err)
	}
	return count, nil
}

// ActiveAgents returns agent names that have claimed identities within the given window.
func (c *Client) ActiveAgents(since time.Duration, repoPrefix string) ([]string, error) {
	cutoff := time.Now().UTC().Add(-since).Format("2006-01-02 15:04:05")

	var names []string
	var err error

	if repoPrefix == "" {
		err = c.db.Select(&names,
			`SELECT repo || '/' || name FROM agent_names WHERE claimed_at >= ?`, cutoff)
	} else {
		err = c.db.Select(&names,
			`SELECT repo || '/' || name FROM agent_names WHERE claimed_at >= ? AND repo = ?`,
			cutoff, repoPrefix)
	}

	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("querying active agents: %w", err)
	}
	return names, nil
}

// Publisher provides write access to the agent-msg SQLite database.
type Publisher struct {
	db *sqlx.DB
}

// NewPublisher opens the agent-msg database in read-write mode for publishing messages,
// with automatic WAL recovery if stale -shm/-wal files are detected.
func NewPublisher(dbPath string) (*Publisher, error) {
	db, err := sqliteutil.OpenWithRecovery(dbPath, dbPath+"?_journal_mode=WAL&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening agent-msg DB for writing: %w", err)
	}
	return &Publisher{db: db}, nil
}

// Publish inserts a message into the agent-msg messages table.
func (p *Publisher) Publish(topic, sender, message string) error {
	_, err := p.db.Exec(
		`INSERT INTO messages (topic, sender, message, created_at) VALUES (?, ?, ?, datetime('now'))`,
		topic, sender, message,
	)
	if err != nil {
		return fmt.Errorf("publishing message to %s: %w", topic, err)
	}
	return nil
}

// PublishReplace deletes all existing messages on the topic, then inserts
// the new message. Used for single-message topics like onboarding where
// only the latest version matters.
func (p *Publisher) PublishReplace(topic, sender, message string) error {
	tx, err := p.db.Beginx()
	if err != nil {
		return fmt.Errorf("begin tx for replace on %s: %w", topic, err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM messages WHERE topic = ?`, topic); err != nil {
		return fmt.Errorf("delete existing messages on %s: %w", topic, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO messages (topic, sender, message, created_at) VALUES (?, ?, ?, datetime('now'))`,
		topic, sender, message,
	); err != nil {
		return fmt.Errorf("insert replacement on %s: %w", topic, err)
	}

	return tx.Commit()
}

// Close closes the publisher's database connection.
func (p *Publisher) Close() error {
	return p.db.Close()
}
