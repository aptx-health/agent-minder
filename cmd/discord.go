package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/dustinlange/agent-minder/internal/api"
	"github.com/dustinlange/agent-minder/internal/discord"
	"github.com/spf13/cobra"
)

var (
	discordToken     string
	discordChannelID string
	discordGuildID   string
)

var discordCmd = &cobra.Command{
	Use:   "discord",
	Short: "Run the Discord bot for deploy daemon interaction",
	Long: `Run a Discord bot that connects to a remote deploy daemon and exposes
slash commands for analysis, status, cost, and settings.

Requires a Discord bot token and the address of a running deploy daemon
with --serve enabled.

See docs/discord-setup.md for full setup instructions.`,
	Example: `  # Run the Discord bot
  agent-minder discord --remote vps:7749 --token $DISCORD_BOT_TOKEN --channel 123456789

  # With guild-scoped commands (faster registration, good for development)
  agent-minder discord --remote vps:7749 --token $DISCORD_BOT_TOKEN --channel 123456789 --guild 987654321

  # Using environment variables
  export MINDER_REMOTE=vps:7749
  export MINDER_API_KEY=secret
  export DISCORD_BOT_TOKEN=your-bot-token
  export DISCORD_CHANNEL_ID=123456789
  agent-minder discord`,
	RunE: runDiscord,
}

func init() {
	rootCmd.AddCommand(discordCmd)

	discordCmd.Flags().StringVar(&discordToken, "token", "", "Discord bot token (or set DISCORD_BOT_TOKEN)")
	discordCmd.Flags().StringVar(&discordChannelID, "channel", "", "Discord channel ID for notifications (or set DISCORD_CHANNEL_ID)")
	discordCmd.Flags().StringVar(&discordGuildID, "guild", "", "Discord guild ID for slash commands — omit for global (or set DISCORD_GUILD_ID)")

	// Reuse the deploy remote/api-key flags.
	discordCmd.Flags().StringVar(&deployRemote, "remote", "", "Deploy daemon address host:port (or set MINDER_REMOTE)")
	discordCmd.Flags().StringVar(&deployAPIKey, "api-key", "", "API key for daemon authentication (or set MINDER_API_KEY)")
}

func runDiscord(_ *cobra.Command, _ []string) error {
	// Resolve config from flags and environment.
	token := discordToken
	if token == "" {
		token = os.Getenv("DISCORD_BOT_TOKEN")
	}
	if token == "" {
		return fmt.Errorf("discord bot token required — set --token or DISCORD_BOT_TOKEN")
	}

	channelID := discordChannelID
	if channelID == "" {
		channelID = os.Getenv("DISCORD_CHANNEL_ID")
	}

	guildID := discordGuildID
	if guildID == "" {
		guildID = os.Getenv("DISCORD_GUILD_ID")
	}

	remote := deployRemote
	if remote == "" {
		remote = os.Getenv("MINDER_REMOTE")
	}
	if remote == "" {
		return fmt.Errorf("deploy daemon address required — set --remote or MINDER_REMOTE")
	}

	apiKey := deployAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("MINDER_API_KEY")
	}

	client := api.NewClient(remote, apiKey)

	// Verify connectivity to the daemon.
	if _, err := client.GetStatus(); err != nil {
		return fmt.Errorf("cannot reach deploy daemon at %s: %w", remote, err)
	}

	bot, err := discord.New(discord.Config{
		Token:     token,
		ChannelID: channelID,
		GuildID:   guildID,
		Client:    client,
	})
	if err != nil {
		return fmt.Errorf("create discord bot: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down Discord bot...", sig)
		cancel()
	}()

	log.Printf("Starting Discord bot (remote: %s, channel: %s)", remote, channelID)
	return bot.Run(ctx)
}
