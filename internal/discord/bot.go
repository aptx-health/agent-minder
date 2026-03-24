// Package discord provides a Discord bot that connects to the agent-minder
// deploy daemon's HTTP API and exposes slash commands for status, analysis,
// cost, and settings. It also receives push notifications via the daemon's
// webhook system.
package discord

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/dustinlange/agent-minder/internal/api"
)

// Bot is a Discord bot backed by the deploy daemon's HTTP API.
type Bot struct {
	session   *discordgo.Session
	client    *api.Client
	channelID string
	guildID   string // empty = global commands
}

// Config holds configuration for the Discord bot.
type Config struct {
	Token     string // Discord bot token
	ChannelID string // Channel for push notifications
	GuildID   string // Guild for slash commands (empty = global)
	Client    *api.Client
}

// New creates a new Discord bot. Call Run() to start it.
func New(cfg Config) (*Bot, error) {
	session, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("create discord session: %w", err)
	}

	bot := &Bot{
		session:   session,
		client:    cfg.Client,
		channelID: cfg.ChannelID,
		guildID:   cfg.GuildID,
	}

	session.AddHandler(bot.handleInteraction)

	return bot, nil
}

// Run opens the Discord connection, registers slash commands, and blocks
// until ctx is cancelled. It cleans up commands on exit.
func (b *Bot) Run(ctx context.Context) error {
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("open discord session: %w", err)
	}
	defer func() { _ = b.session.Close() }()

	registered, err := b.registerCommands()
	if err != nil {
		return fmt.Errorf("register commands: %w", err)
	}
	log.Printf("Discord bot ready (%d commands registered)", len(registered))

	<-ctx.Done()

	// Clean up registered commands.
	for _, cmd := range registered {
		if err := b.session.ApplicationCommandDelete(b.session.State.User.ID, b.guildID, cmd.ID); err != nil {
			log.Printf("Failed to delete command %s: %v", cmd.Name, err)
		}
	}
	return nil
}

// registerCommands registers all slash commands with Discord.
func (b *Bot) registerCommands() ([]*discordgo.ApplicationCommand, error) {
	commands := []*discordgo.ApplicationCommand{
		{
			Name:        "analysis",
			Description: "Trigger a live LLM analysis of the deployment",
		},
		{
			Name:        "status",
			Description: "Show current task status",
		},
		{
			Name:        "settings",
			Description: "Show autopilot and review settings",
		},
		{
			Name:        "cost",
			Description: "Show cost overview (daily, weekly, project)",
		},
	}

	var registered []*discordgo.ApplicationCommand
	for _, cmd := range commands {
		created, err := b.session.ApplicationCommandCreate(b.session.State.User.ID, b.guildID, cmd)
		if err != nil {
			return registered, fmt.Errorf("register /%s: %w", cmd.Name, err)
		}
		registered = append(registered, created)
	}
	return registered, nil
}

// handleInteraction routes slash command interactions to the appropriate handler.
func (b *Bot) handleInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionApplicationCommand {
		return
	}

	switch i.ApplicationCommandData().Name {
	case "analysis":
		b.handleAnalysis(s, i)
	case "status":
		b.handleStatus(s, i)
	case "settings":
		b.handleSettings(s, i)
	case "cost":
		b.handleCost(s, i)
	}
}

// respond sends an initial interaction response.
func respond(s *discordgo.Session, i *discordgo.InteractionCreate, embeds []*discordgo.MessageEmbed) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: embeds,
		},
	})
}

// respondEphemeral sends an ephemeral (only-visible-to-requester) response.
func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, embeds []*discordgo.MessageEmbed) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: embeds,
			Flags:  discordgo.MessageFlagsEphemeral,
		},
	})
}

// respondDeferred sends a "thinking..." deferred response.
func respondDeferred(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})
}

// editResponse updates a deferred response with actual content.
func editResponse(s *discordgo.Session, i *discordgo.InteractionCreate, embeds []*discordgo.MessageEmbed) {
	_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Embeds: &embeds,
	})
}

// pollForAnalysis waits for a fresh analysis result after triggering a poll.
func (b *Bot) pollForAnalysis(startTime time.Time) (*api.AnalysisResponse, error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	timeout := time.After(3 * time.Minute)
	for {
		select {
		case <-timeout:
			return nil, fmt.Errorf("timed out waiting for analysis")
		case <-ticker.C:
			results, err := b.client.GetAnalysis(1)
			if err != nil {
				continue
			}
			if len(results) > 0 {
				polledAt, err := time.Parse("2006-01-02 15:04:05", results[0].PolledAt)
				if err == nil && polledAt.After(startTime.Add(-2*time.Second)) {
					return &results[0], nil
				}
			}
		}
	}
}
