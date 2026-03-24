// test-discord starts a standalone API server against the real minder.db
// so the Discord bot can be tested without a running daemon.
//
// Usage:
//
//	go run scripts/test-discord.go [--project NAME] [--port PORT]
//
// Then in another terminal:
//
//	agent-minder discord --remote localhost:7749 --token $DISCORD_BOT_TOKEN --channel $DISCORD_CHANNEL_ID --guild $DISCORD_GUILD_ID
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/dustinlange/agent-minder/internal/api"
	"github.com/dustinlange/agent-minder/internal/db"
)

func main() {
	project := flag.String("project", "", "Project name to serve (lists available if omitted)")
	port := flag.String("port", "7749", "Port to listen on")
	flag.Parse()

	dbPath := os.Getenv("MINDER_DB")
	if dbPath == "" {
		dbPath = db.DefaultDBPath()
	}

	conn, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("Open DB: %v", err)
	}
	defer func() { _ = conn.Close() }()
	store := db.NewStore(conn)

	// If no project specified, list available ones.
	if *project == "" {
		projects, err := store.ListProjects()
		if err != nil {
			log.Fatalf("List projects: %v", err)
		}
		deploys, err := store.ListDeployProjects()
		if err != nil {
			log.Fatalf("List deploy projects: %v", err)
		}

		fmt.Println("Available projects:")
		for _, p := range projects {
			fmt.Printf("  %-25s (id=%d, type=%s)\n", p.Name, p.ID, p.GoalType)
		}
		for _, p := range deploys {
			fmt.Printf("  %-25s (id=%d, deploy)\n", p.Name, p.ID)
		}
		fmt.Println("\nUsage: go run scripts/test-discord.go --project <name>")
		os.Exit(0)
	}

	// Look up the project.
	proj, err := store.GetProject(*project)
	if err != nil {
		log.Fatalf("Project %q not found: %v", *project, err)
	}

	log.Printf("Serving project %q (id=%d) on :%s", proj.Name, proj.ID, *port)
	log.Printf("DB: %s", dbPath)
	log.Println()
	log.Println("Test the API:")
	log.Printf("  curl http://localhost:%s/status", *port)
	log.Printf("  curl http://localhost:%s/tasks", *port)
	log.Printf("  curl http://localhost:%s/metrics", *port)
	log.Printf("  curl http://localhost:%s/analysis", *port)
	log.Println()
	log.Println("Run the Discord bot:")
	log.Printf("  agent-minder discord --remote localhost:%s --token $DISCORD_BOT_TOKEN --channel $DISCORD_CHANNEL_ID --guild $DISCORD_GUILD_ID", *port)

	srv := api.New(api.Config{
		Store:     store,
		ProjectID: proj.ID,
		DeployID:  proj.Name,
		TriggerPoll: func() error {
			log.Println("[mock] Analysis poll triggered (no-op in test mode)")
			return nil
		},
		StopDaemon: func() {
			log.Println("[mock] Stop requested (no-op in test mode)")
		},
		BudgetResume: func() {
			log.Println("[mock] Budget resume requested (no-op in test mode)")
		},
		IsBudgetPaused: func() bool { return false },
	})

	// Signal handling.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		log.Println("Shutting down...")
		os.Exit(0)
	}()

	addr := ":" + *port
	if err := srv.ListenAndServe(addr); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}
