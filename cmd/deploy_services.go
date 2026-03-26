package cmd

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/dustinlange/agent-minder/internal/api"
	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/db"
	"github.com/dustinlange/agent-minder/internal/discord"
	"github.com/spf13/viper"
)

// daemonServices holds the HTTP API server and Discord bot started alongside
// a deploy daemon. Call Shutdown() during teardown.
type daemonServices struct {
	apiServer     *api.Server
	discordCancel context.CancelFunc
}

// Shutdown gracefully stops the Discord bot and API server.
func (s *daemonServices) Shutdown() {
	if s.discordCancel != nil {
		s.discordCancel()
	}
	if s.apiServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.apiServer.Shutdown(ctx); err != nil {
			log.Printf("API server shutdown error: %v", err)
		}
	}
}

// serviceConfig holds the parameters needed to start daemon services.
type serviceConfig struct {
	Store      *db.Store
	ProjectID  int64
	DeployID   string
	StopDaemon func()

	// Optional autopilot callbacks — nil if not applicable.
	TriggerPoll    func() error
	BudgetResume   func()
	IsBudgetPaused func() bool
}

// startDaemonServices starts the HTTP API server and (if configured) the
// Discord bot. Returns a daemonServices handle for shutdown, or nil if
// --serve was not requested.
func startDaemonServices(ctx context.Context, cfg serviceConfig) *daemonServices {
	serveAddr := deployServe
	if serveAddr == "" {
		serveAddr = os.Getenv("MINDER_SERVE")
	}
	if serveAddr == "" {
		return nil
	}

	apiKeyVal := deployAPIKey
	if apiKeyVal == "" {
		apiKeyVal = os.Getenv("MINDER_API_KEY")
	}

	svc := &daemonServices{}

	// Start HTTP API server.
	svc.apiServer = api.New(api.Config{
		Store:          cfg.Store,
		ProjectID:      cfg.ProjectID,
		DeployID:       cfg.DeployID,
		APIKey:         apiKeyVal,
		BindAddr:       serveAddr,
		TriggerPoll:    cfg.TriggerPoll,
		StopDaemon:     cfg.StopDaemon,
		BudgetResume:   cfg.BudgetResume,
		IsBudgetPaused: cfg.IsBudgetPaused,
	})
	go func() {
		if err := svc.apiServer.ListenAndServe(serveAddr); err != nil && err != http.ErrServerClosed {
			log.Printf("API server error: %v", err)
		}
	}()

	// Start embedded Discord bot if credentials are configured.
	svc.discordCancel = startEmbeddedDiscord(ctx, serveAddr, apiKeyVal)

	return svc
}

// startEmbeddedDiscord creates and runs a Discord bot in-process using a
// loopback API client. Returns a cancel func to stop it, or nil if Discord
// is not configured.
func startEmbeddedDiscord(ctx context.Context, serveAddr, apiKey string) context.CancelFunc {
	// Load config so viper has discord channel/guild IDs.
	_, _ = config.Load()

	token := config.GetIntegrationToken("discord")
	if token == "" {
		return nil
	}

	channelID := viper.GetString("integrations.discord.channel_id")
	guildID := viper.GetString("integrations.discord.guild_id")

	// Build loopback client to the API server we just started.
	client := api.NewClient("localhost"+serveAddr, apiKey)

	bot, err := discord.New(discord.Config{
		Token:     token,
		ChannelID: channelID,
		GuildID:   guildID,
		Client:    client,
	})
	if err != nil {
		log.Printf("Discord bot init error (skipping): %v", err)
		return nil
	}

	discordCtx, discordCancel := context.WithCancel(ctx)
	go func() {
		log.Printf("Starting embedded Discord bot (channel: %s)", channelID)
		if err := bot.Run(discordCtx); err != nil {
			log.Printf("Discord bot error: %v", err)
		}
	}()

	return discordCancel
}
