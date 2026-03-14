package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/dustinlange/agent-minder/internal/secrets"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var showProviders bool

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Configure providers and integrations",
	Long:  "Interactive wizard to set up LLM providers (API keys) and integrations (GitHub, etc.).",
	RunE:  runSetup,
}

func init() {
	setupCmd.Flags().BoolVar(&showProviders, "show-providers", false, "Show configured providers and their credential sources")
	rootCmd.AddCommand(setupCmd)
}

func runSetup(cmd *cobra.Command, args []string) error {
	if showProviders {
		return runShowProviders()
	}

	reader := bufio.NewReader(os.Stdin)

	fmt.Println("agent-minder setup")
	fmt.Println(strings.Repeat("─", 40))

	// Migrate plaintext credentials to keychain if available.
	if config.KeychainAvailable() {
		migrateCredentials(reader)
	}

	// Show current state.
	providers := config.ConfiguredProviders()
	integrations := config.ConfiguredIntegrations()

	if len(providers) > 0 || len(integrations) > 0 {
		fmt.Println("\nCurrent configuration:")
		for _, p := range providers {
			key := config.GetProviderAPIKey(p)
			src := config.TokenSource("provider", p)
			fmt.Printf("  Provider: %s  [%s] (%s)\n", p, maskKey(key), src)
		}
		for _, i := range integrations {
			token := config.GetIntegrationToken(i)
			src := config.TokenSource("integration", i)
			fmt.Printf("  Integration: %s  [%s] (%s)\n", i, maskKey(token), src)
		}
		fmt.Println()
	}

	// --- LLM Providers ---
	fmt.Println("\n── LLM Providers ──")
	fmt.Println("Configure API keys for AI providers. These are used for the")
	fmt.Println("summarizer (tier 1) and analyzer (tier 2) LLM calls.")

	setupProvider(reader, "anthropic", "Anthropic", "https://console.anthropic.com/settings/keys")
	setupProvider(reader, "openai", "OpenAI", "https://platform.openai.com/api-keys")

	// --- Integrations ---
	fmt.Println("\n── Integrations ──")
	fmt.Println("Configure tokens for external services.")

	setupGitHub(reader)

	// Save.
	if err := config.Save(); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Printf("\nConfig saved to %s\n", config.ConfigPath())

	// Summary.
	fmt.Println("\nConfigured:")
	for _, p := range config.ConfiguredProviders() {
		src := config.TokenSource("provider", p)
		fmt.Printf("  ✓ Provider: %s (%s)\n", p, src)
	}
	for _, i := range config.ConfiguredIntegrations() {
		src := config.TokenSource("integration", i)
		fmt.Printf("  ✓ Integration: %s (%s)\n", i, src)
	}
	if len(config.ConfiguredProviders()) == 0 {
		fmt.Println("  (no providers — set ANTHROPIC_API_KEY env var or re-run setup)")
	}

	return nil
}

// runShowProviders prints a table of configured credentials and their sources.
func runShowProviders() error {
	fmt.Printf("%-12s %-10s %s\n", "Name", "Source", "Status")
	fmt.Println(strings.Repeat("─", 40))

	for _, p := range []string{"anthropic", "openai"} {
		src := config.TokenSource("provider", p)
		if src == "" {
			fmt.Printf("%-12s %-10s %s\n", p, "-", "not configured")
		} else {
			status := "configured"
			if src == "config" {
				status = "configured (plaintext)"
			}
			fmt.Printf("%-12s %-10s %s\n", p, src, status)
		}
	}
	for _, i := range []string{"github"} {
		src := config.TokenSource("integration", i)
		if src == "" {
			fmt.Printf("%-12s %-10s %s\n", i, "-", "not configured")
		} else {
			status := "configured"
			if src == "config" {
				status = "configured (plaintext)"
			}
			fmt.Printf("%-12s %-10s %s\n", i, src, status)
		}
	}
	return nil
}

// migrateCredentials prompts to move plaintext config file credentials to keychain.
func migrateCredentials(reader *bufio.Reader) {
	type entry struct {
		keyType string
		name    string
		label   string
	}
	entries := []entry{
		{"provider", "anthropic", "Anthropic API key"},
		{"provider", "openai", "OpenAI API key"},
		{"integration", "github", "GitHub token"},
	}

	migrated := false
	for _, e := range entries {
		var inConfig bool
		switch e.keyType {
		case "provider":
			inConfig = config.ProviderKeyInConfig(e.name)
		case "integration":
			inConfig = config.IntegrationTokenInConfig(e.name)
		}
		if !inConfig {
			continue
		}

		// Skip if already in keychain.
		switch e.keyType {
		case "provider":
			if config.ProviderKeyInKeychain(e.name) {
				continue
			}
		case "integration":
			if config.IntegrationTokenInKeychain(e.name) {
				continue
			}
		}

		fmt.Printf("Migrate %s to secure keychain? [Y/n]: ", e.label)
		if readNo(reader) {
			continue
		}

		if err := config.MigrateToKeychain(e.keyType, e.name); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: migration failed: %v\n", err)
			continue
		}
		fmt.Printf("  ✓ %s migrated to keychain\n", e.label)
		migrated = true
	}

	if migrated {
		if err := config.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: saving config after migration: %v\n", err)
		}
		fmt.Println()
	}
}

func setupProvider(reader *bufio.Reader, name, label, helpURL string) {
	existing := config.GetProviderAPIKey(name)
	if existing != "" {
		fmt.Printf("\n%s: configured [%s]\n", label, maskKey(existing))
		fmt.Printf("  Update? [y/N]: ")
		if !readYes(reader) {
			return
		}
	} else {
		fmt.Printf("\n%s: not configured\n", label)
		fmt.Printf("  Get a key at: %s\n", helpURL)
		fmt.Printf("  Configure now? [Y/n]: ")
		if readNo(reader) {
			return
		}
	}

	fmt.Printf("  API key: ")
	key := readLineSensitive(reader)
	if key == "" {
		fmt.Println("  Skipped.")
		return
	}

	if secrets.Available() {
		if err := config.SetProviderAPIKeySecure(name, key); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: keychain write failed: %v — falling back to config file\n", err)
			config.SetProviderAPIKey(name, key)
			fmt.Printf("  ✓ %s configured (config file)\n", label)
		} else {
			// Clear from config file if previously stored there.
			config.RemoveProviderAPIKeyFromConfig(name)
			fmt.Printf("  ✓ %s configured (keychain)\n", label)
		}
	} else {
		config.SetProviderAPIKey(name, key)
		fmt.Printf("  ✓ %s configured (config file)\n", label)
	}

	// Optional base URL (useful for OpenAI-compatible providers).
	if name == "openai" {
		existingURL := config.GetProviderBaseURL(name)
		if existingURL != "" {
			fmt.Printf("  Base URL [%s]: ", existingURL)
		} else {
			fmt.Printf("  Base URL (blank for default): ")
		}
		url := readLine(reader)
		if url != "" {
			config.SetProviderBaseURL(name, url)
		}
	}
}

func setupGitHub(reader *bufio.Reader) {
	existing := config.GetIntegrationToken("github")
	if existing != "" {
		fmt.Printf("\nGitHub: configured [%s]\n", maskKey(existing))
		fmt.Printf("  Update? [y/N]: ")
		if !readYes(reader) {
			return
		}
	} else {
		fmt.Printf("\nGitHub: not configured\n")
		fmt.Println("  Used for tracking issues and pull requests.")
		fmt.Println()
		fmt.Println("  Create a Fine-grained Personal Access Token:")
		fmt.Println("    1. Go to: https://github.com/settings/personal-access-tokens/new")
		fmt.Println("    2. Name: agent-minder")
		fmt.Println("    3. Repository access: select your repos")
		fmt.Println("    4. Permissions:")
		fmt.Println("       - Issues: Read")
		fmt.Println("       - Pull requests: Read")
		fmt.Println("    5. Generate token")
		fmt.Println()
		fmt.Printf("  Configure now? [Y/n]: ")
		if readNo(reader) {
			return
		}
	}

	fmt.Printf("  Token: ")
	token := readLineSensitive(reader)
	if token == "" {
		fmt.Println("  Skipped.")
		return
	}

	if secrets.Available() {
		if err := config.SetIntegrationTokenSecure("github", token); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: keychain write failed: %v — falling back to config file\n", err)
			config.SetIntegrationToken("github", token)
			fmt.Printf("  ✓ GitHub configured (config file)\n")
		} else {
			config.RemoveIntegrationTokenFromConfig("github")
			fmt.Printf("  ✓ GitHub configured (keychain)\n")
		}
	} else {
		config.SetIntegrationToken("github", token)
		fmt.Printf("  ✓ GitHub configured (config file)\n")
	}
}

// maskKey shows first 4 and last 4 chars of a key, masking the rest.
func maskKey(key string) string {
	if len(key) <= 12 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// readYes returns true if the user typed "y" or "yes".
func readYes(reader *bufio.Reader) bool {
	line := readLine(reader)
	return strings.HasPrefix(strings.ToLower(line), "y")
}

// readNo returns true if the user typed "n" or "no".
func readNo(reader *bufio.Reader) bool {
	line := readLine(reader)
	return strings.HasPrefix(strings.ToLower(line), "n")
}

// readLineSensitive reads a secret without echoing to the terminal.
// Falls back to plain readLine if stdin is not a terminal (e.g., piped input).
func readLineSensitive(_ *bufio.Reader) string {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// Not a terminal — fall back to reading from the reader.
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		return strings.TrimSpace(line)
	}
	secret, err := term.ReadPassword(fd)
	fmt.Println() // newline after hidden input
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(secret))
}
