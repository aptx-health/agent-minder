package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/aptx-health/agent-minder/internal/auth"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(authLoginCmd, authStatusCmd, authLogoutCmd)
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage GitHub token authentication",
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Store GitHub token in the OS keyring",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Print("Enter GitHub token (ghp_...): ")

		// Try to read without echo (terminal), fall back to plain read (pipe).
		var token string
		if term.IsTerminal(int(os.Stdin.Fd())) {
			raw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println() // newline after hidden input
			if err != nil {
				return fmt.Errorf("read token: %w", err)
			}
			token = string(raw)
		} else {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				token = scanner.Text()
			}
		}

		token = strings.TrimSpace(token)
		if token == "" {
			return fmt.Errorf("no token provided")
		}

		if err := auth.SetToken(token); err != nil {
			return fmt.Errorf("store token: %w", err)
		}

		fmt.Println("Token stored in OS keyring.")
		return nil
	},
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check if a GitHub token is configured",
	RunE: func(cmd *cobra.Command, args []string) error {
		if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
			fmt.Printf("GITHUB_TOKEN: set via environment (%s...%s)\n", tok[:4], tok[len(tok)-4:])
			return nil
		}

		if auth.HasKeyringToken() {
			tok, _ := auth.GetToken()
			if len(tok) >= 8 {
				fmt.Printf("GITHUB_TOKEN: stored in keyring (%s...%s)\n", tok[:4], tok[len(tok)-4:])
			} else {
				fmt.Println("GITHUB_TOKEN: stored in keyring")
			}
			return nil
		}

		fmt.Println("GITHUB_TOKEN: not configured")
		fmt.Println("  Run: minder auth login")
		fmt.Println("  Or:  export GITHUB_TOKEN=ghp_...")
		return nil
	},
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove GitHub token from the OS keyring",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := auth.DeleteToken(); err != nil {
			return fmt.Errorf("remove token: %w", err)
		}
		fmt.Println("Token removed from OS keyring.")
		return nil
	},
}
