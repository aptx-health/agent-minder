// Package config manages global application configuration via Viper.
// Config file lives at ~/.agent-minder/config.yaml and stores provider
// credentials, integration tokens, and global settings.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// ProviderConfig holds credentials for an LLM provider.
type ProviderConfig struct {
	APIKey  string `mapstructure:"api_key"`
	BaseURL string `mapstructure:"base_url"`
}

// IntegrationConfig holds credentials for an external integration.
type IntegrationConfig struct {
	Token   string `mapstructure:"token"`
	BaseURL string `mapstructure:"base_url"`
}

// Config is the top-level application configuration.
type Config struct {
	Providers    map[string]ProviderConfig    `mapstructure:"providers"`
	Integrations map[string]IntegrationConfig `mapstructure:"integrations"`
}

// BaseDir returns the root config directory (~/.agent-minder).
func BaseDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".agent-minder"), nil
}

// ConfigPath returns the full path to the config file.
func ConfigPath() string {
	base, err := BaseDir()
	if err != nil {
		return "config.yaml" // last resort; Init() will log a warning
	}
	return filepath.Join(base, "config.yaml")
}

// Init sets up Viper to read from the config file and environment.
// Call this once at application startup (e.g., in root command's PersistentPreRun).
func Init() {
	base, err := BaseDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		return
	}
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(base)

	// Environment variable fallbacks — AGENT_MINDER_ prefix.
	viper.SetEnvPrefix("AGENT_MINDER")
	viper.AutomaticEnv()

	// Legacy env var mappings for backward compatibility.
	// ANTHROPIC_API_KEY -> providers.anthropic.api_key
	// OPENAI_API_KEY -> providers.openai.api_key
	// GITHUB_TOKEN -> integrations.github.token

	if err := viper.ReadInConfig(); err != nil {
		// Config file not found is fine — we'll use env vars / defaults.
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			fmt.Fprintf(os.Stderr, "Warning: reading config: %v\n", err)
		}
	}
}

// Load reads the current config into a Config struct.
func Load() (*Config, error) {
	cfg := &Config{
		Providers:    make(map[string]ProviderConfig),
		Integrations: make(map[string]IntegrationConfig),
	}
	if err := viper.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return cfg, nil
}

// Save writes the current Viper state to the config file with restricted permissions.
func Save() error {
	base, err := BaseDir()
	if err != nil {
		return err
	}
	path := filepath.Join(base, "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	if err := viper.WriteConfigAs(path); err != nil {
		return err
	}
	// Lock down permissions — file contains secrets.
	return os.Chmod(path, 0600)
}

// GetProviderAPIKey returns the API key for a provider, checking:
// 1. Viper config (file + env)
// 2. Legacy environment variables (ANTHROPIC_API_KEY, OPENAI_API_KEY)
func GetProviderAPIKey(provider string) string {
	// Check Viper config first.
	key := viper.GetString("providers." + provider + ".api_key")
	if key != "" {
		return key
	}

	// Fall back to legacy env vars.
	switch provider {
	case "anthropic":
		return os.Getenv("ANTHROPIC_API_KEY")
	case "openai":
		return os.Getenv("OPENAI_API_KEY")
	}

	return ""
}

// GetProviderBaseURL returns the base URL for a provider, if configured.
func GetProviderBaseURL(provider string) string {
	return viper.GetString("providers." + provider + ".base_url")
}

// GetIntegrationToken returns the token for an integration, checking:
// 1. Viper config (file + env)
// 2. Legacy environment variables (GITHUB_TOKEN, GH_TOKEN)
func GetIntegrationToken(integration string) string {
	token := viper.GetString("integrations." + integration + ".token")
	if token != "" {
		return token
	}

	// Fall back to legacy env vars.
	switch integration {
	case "github":
		if t := os.Getenv("GITHUB_TOKEN"); t != "" {
			return t
		}
		return os.Getenv("GH_TOKEN")
	}

	return ""
}

// GetIntegrationBaseURL returns the base URL for an integration, if configured.
func GetIntegrationBaseURL(integration string) string {
	return viper.GetString("integrations." + integration + ".base_url")
}

// SetProviderAPIKey sets a provider's API key in Viper (call Save() to persist).
func SetProviderAPIKey(provider, apiKey string) {
	viper.Set("providers."+provider+".api_key", apiKey)
}

// SetProviderBaseURL sets a provider's base URL in Viper (call Save() to persist).
func SetProviderBaseURL(provider, baseURL string) {
	viper.Set("providers."+provider+".base_url", baseURL)
}

// SetIntegrationToken sets an integration token in Viper (call Save() to persist).
func SetIntegrationToken(integration, token string) {
	viper.Set("integrations."+integration+".token", token)
}

// SetIntegrationBaseURL sets an integration base URL in Viper (call Save() to persist).
func SetIntegrationBaseURL(integration, baseURL string) {
	viper.Set("integrations."+integration+".base_url", baseURL)
}

// HasProvider returns whether a provider has any configuration.
func HasProvider(provider string) bool {
	return GetProviderAPIKey(provider) != ""
}

// HasIntegration returns whether an integration has any configuration.
func HasIntegration(integration string) bool {
	return GetIntegrationToken(integration) != ""
}

// ConfiguredProviders returns a list of provider names that have API keys configured.
func ConfiguredProviders() []string {
	var providers []string
	for _, name := range []string{"anthropic", "openai"} {
		if HasProvider(name) {
			providers = append(providers, name)
		}
	}
	return providers
}

// ConfiguredIntegrations returns a list of integration names that have tokens configured.
func ConfiguredIntegrations() []string {
	var integrations []string
	for _, name := range []string{"github"} {
		if HasIntegration(name) {
			integrations = append(integrations, name)
		}
	}
	return integrations
}
