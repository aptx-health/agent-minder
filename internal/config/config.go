// Package config manages global application configuration via Viper.
// Config file lives at ~/.agent-minder/config.yaml and stores provider
// credentials, integration tokens, and global settings.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/dustinlange/agent-minder/internal/secrets"
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

// configWarnings tracks which plaintext-config warnings have been emitted.
var configWarnings = struct {
	sync.Mutex
	seen map[string]bool
}{seen: make(map[string]bool)}

func warnPlaintextOnce(keyType, name string) {
	key := keyType + "/" + name
	configWarnings.Lock()
	defer configWarnings.Unlock()
	if configWarnings.seen[key] {
		return
	}
	configWarnings.seen[key] = true
	fmt.Fprintf(os.Stderr, "Warning: %s %s API key loaded from plaintext config file. Run 'agent-minder setup' to migrate to secure keychain.\n", name, keyType)
}

// ResetWarnings clears the warning dedup state (for tests).
func ResetWarnings() {
	configWarnings.Lock()
	defer configWarnings.Unlock()
	configWarnings.seen = make(map[string]bool)
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
// 1. Keychain (via secrets package)
// 2. Env vars: legacy (ANTHROPIC_API_KEY, OPENAI_API_KEY) + AGENT_MINDER_* via Viper
// 3. Config file (with plaintext warning)
func GetProviderAPIKey(provider string) string {
	// 1. Keychain.
	if val, err := secrets.GetSecret("provider/" + provider); err == nil && val != "" {
		return val
	}

	// 2. Legacy env vars.
	switch provider {
	case "anthropic":
		if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
			return v
		}
	case "openai":
		if v := os.Getenv("OPENAI_API_KEY"); v != "" {
			return v
		}
	}

	// Check AGENT_MINDER_* env vars (Viper binds these automatically).
	// We need to distinguish env from file — check the env var directly.
	envKey := fmt.Sprintf("AGENT_MINDER_PROVIDERS_%s_API_KEY", upperProvider(provider))
	if v := os.Getenv(envKey); v != "" {
		return v
	}

	// 3. Config file.
	key := viper.GetString("providers." + provider + ".api_key")
	if key != "" {
		warnPlaintextOnce("provider", provider)
		return key
	}

	return ""
}

// GetProviderBaseURL returns the base URL for a provider, if configured.
func GetProviderBaseURL(provider string) string {
	return viper.GetString("providers." + provider + ".base_url")
}

// GetIntegrationToken returns the token for an integration, checking:
// 1. Keychain (via secrets package)
// 2. Env vars: legacy (GITHUB_TOKEN, GH_TOKEN) + AGENT_MINDER_*
// 3. Config file (with plaintext warning)
func GetIntegrationToken(integration string) string {
	// 1. Keychain.
	if val, err := secrets.GetSecret("integration/" + integration); err == nil && val != "" {
		return val
	}

	// 2. Legacy env vars.
	switch integration {
	case "github":
		if t := os.Getenv("GITHUB_TOKEN"); t != "" {
			return t
		}
		if t := os.Getenv("GH_TOKEN"); t != "" {
			return t
		}
	}

	// Check AGENT_MINDER_* env vars.
	envKey := fmt.Sprintf("AGENT_MINDER_INTEGRATIONS_%s_TOKEN", upperProvider(integration))
	if v := os.Getenv(envKey); v != "" {
		return v
	}

	// 3. Config file.
	token := viper.GetString("integrations." + integration + ".token")
	if token != "" {
		warnPlaintextOnce("integration", integration)
		return token
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

// SetProviderAPIKeySecure stores a provider API key in the OS keychain.
func SetProviderAPIKeySecure(provider, apiKey string) error {
	return secrets.SetSecret("provider/"+provider, apiKey)
}

// SetIntegrationTokenSecure stores an integration token in the OS keychain.
func SetIntegrationTokenSecure(name, token string) error {
	return secrets.SetSecret("integration/"+name, token)
}

// RemoveProviderAPIKeyFromConfig clears a provider's API key from the config file.
func RemoveProviderAPIKeyFromConfig(provider string) {
	viper.Set("providers."+provider+".api_key", "")
}

// RemoveIntegrationTokenFromConfig clears an integration token from the config file.
func RemoveIntegrationTokenFromConfig(name string) {
	viper.Set("integrations."+name+".token", "")
}

// TokenSource returns where a credential is sourced from:
// "keychain", "env", "config", or "" (not found).
func TokenSource(keyType, name string) string {
	// Check keychain.
	secretKey := keyType + "/" + name
	if val, err := secrets.GetSecret(secretKey); err == nil && val != "" {
		return "keychain"
	}

	// Check env vars.
	if hasEnvVar(keyType, name) {
		return "env"
	}

	// Check config file.
	var viperKey string
	switch keyType {
	case "provider":
		viperKey = "providers." + name + ".api_key"
	case "integration":
		viperKey = "integrations." + name + ".token"
	}
	if viperKey != "" && viper.GetString(viperKey) != "" {
		return "config"
	}

	return ""
}

// hasEnvVar checks if a credential is available via environment variables.
func hasEnvVar(keyType, name string) bool {
	switch keyType {
	case "provider":
		switch name {
		case "anthropic":
			return os.Getenv("ANTHROPIC_API_KEY") != ""
		case "openai":
			return os.Getenv("OPENAI_API_KEY") != ""
		}
		envKey := fmt.Sprintf("AGENT_MINDER_PROVIDERS_%s_API_KEY", upperProvider(name))
		return os.Getenv(envKey) != ""
	case "integration":
		switch name {
		case "github":
			return os.Getenv("GITHUB_TOKEN") != "" || os.Getenv("GH_TOKEN") != ""
		}
		envKey := fmt.Sprintf("AGENT_MINDER_INTEGRATIONS_%s_TOKEN", upperProvider(name))
		return os.Getenv(envKey) != ""
	}
	return false
}

// upperProvider returns the uppercased provider name for env var construction.
func upperProvider(name string) string {
	// Simple uppercase — provider names are short ASCII strings.
	b := make([]byte, len(name))
	for i := range name {
		c := name[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
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

// ProviderKeyInConfig returns true if a provider has an API key stored in the config file
// (not keychain, not env). Used for migration prompts.
func ProviderKeyInConfig(provider string) bool {
	return viper.GetString("providers."+provider+".api_key") != ""
}

// IntegrationTokenInConfig returns true if an integration has a token stored in the config file.
func IntegrationTokenInConfig(name string) bool {
	return viper.GetString("integrations."+name+".token") != ""
}

// ProviderKeyInKeychain returns true if a provider has an API key in the keychain.
func ProviderKeyInKeychain(provider string) bool {
	val, err := secrets.GetSecret("provider/" + provider)
	return err == nil && val != ""
}

// IntegrationTokenInKeychain returns true if an integration has a token in the keychain.
func IntegrationTokenInKeychain(name string) bool {
	val, err := secrets.GetSecret("integration/" + name)
	return err == nil && val != ""
}

// KeychainAvailable returns whether the OS keychain is usable.
func KeychainAvailable() bool {
	return secrets.Available()
}

// MigrateToKeychain moves a credential from config file to keychain.
// Returns an error wrapping ErrNotFound if the value isn't in config.
func MigrateToKeychain(keyType, name string) error {
	var val string
	switch keyType {
	case "provider":
		val = viper.GetString("providers." + name + ".api_key")
		if val == "" {
			return errors.New("no value in config for provider " + name)
		}
		if err := SetProviderAPIKeySecure(name, val); err != nil {
			return fmt.Errorf("writing to keychain: %w", err)
		}
		RemoveProviderAPIKeyFromConfig(name)
	case "integration":
		val = viper.GetString("integrations." + name + ".token")
		if val == "" {
			return errors.New("no value in config for integration " + name)
		}
		if err := SetIntegrationTokenSecure(name, val); err != nil {
			return fmt.Errorf("writing to keychain: %w", err)
		}
		RemoveIntegrationTokenFromConfig(name)
	default:
		return fmt.Errorf("unknown key type: %s", keyType)
	}
	return nil
}
