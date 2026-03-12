package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/dustinlange/agent-minder/internal/config"
	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Configure providers and integrations",
	Long:  "Interactive wizard to set up LLM providers (API keys) and integrations (GitHub, etc.).",
	RunE:  runSetup,
}

func init() {
	rootCmd.AddCommand(setupCmd)
}

func runSetup(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("agent-minder setup")
	fmt.Println(strings.Repeat("─", 40))

	// Show current state.
	providers := config.ConfiguredProviders()
	integrations := config.ConfiguredIntegrations()

	if len(providers) > 0 || len(integrations) > 0 {
		fmt.Println("\nCurrent configuration:")
		for _, p := range providers {
			key := config.GetProviderAPIKey(p)
			fmt.Printf("  Provider: %s  [%s]\n", p, maskKey(key))
		}
		for _, i := range integrations {
			token := config.GetIntegrationToken(i)
			fmt.Printf("  Integration: %s  [%s]\n", i, maskKey(token))
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
		fmt.Printf("  ✓ Provider: %s\n", p)
	}
	for _, i := range config.ConfiguredIntegrations() {
		fmt.Printf("  ✓ Integration: %s\n", i)
	}
	if len(config.ConfiguredProviders()) == 0 {
		fmt.Println("  (no providers — set ANTHROPIC_API_KEY env var or re-run setup)")
	}

	return nil
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
	config.SetProviderAPIKey(name, key)

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

	fmt.Printf("  ✓ %s configured\n", label)
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
	config.SetIntegrationToken("github", token)
	fmt.Printf("  ✓ GitHub configured\n")
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

// readLineSensitive reads a line, trimming whitespace. In the future this
// could disable echo for secret input.
func readLineSensitive(reader *bufio.Reader) string {
	return readLine(reader)
}
