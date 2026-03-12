package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dustinlange/agent-minder/internal/secrets"
	"github.com/spf13/viper"
)

func resetViper(t *testing.T) {
	t.Helper()
	viper.Reset()
	ResetWarnings()
}

func setupTestKeyring(t *testing.T) *secrets.MapKeyring {
	t.Helper()
	mk := secrets.NewMapKeyring()
	secrets.SetDefault(mk)
	t.Cleanup(func() { secrets.SetDefault(secrets.OSKeyring{}) })
	return mk
}

// --- Original tests (updated to use MapKeyring) ---

func TestGetProviderAPIKey_FromConfig(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	viper.Set("providers.anthropic.api_key", "sk-ant-test123")

	got := GetProviderAPIKey("anthropic")
	if got != "sk-ant-test123" {
		t.Errorf("GetProviderAPIKey(anthropic) = %q, want %q", got, "sk-ant-test123")
	}
}

func TestGetProviderAPIKey_FallbackEnv(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-env-fallback")

	got := GetProviderAPIKey("anthropic")
	if got != "sk-env-fallback" {
		t.Errorf("GetProviderAPIKey(anthropic) = %q, want %q", got, "sk-env-fallback")
	}
}

func TestGetProviderAPIKey_KeychainOverridesAll(t *testing.T) {
	resetViper(t)
	mk := setupTestKeyring(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	viper.Set("providers.anthropic.api_key", "sk-config")
	mk.Set("agent-minder", "provider/anthropic", "sk-keychain")

	got := GetProviderAPIKey("anthropic")
	if got != "sk-keychain" {
		t.Errorf("GetProviderAPIKey(anthropic) = %q, want keychain value %q", got, "sk-keychain")
	}
}

func TestGetProviderAPIKey_EnvOverridesConfig(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	viper.Set("providers.anthropic.api_key", "sk-config")

	got := GetProviderAPIKey("anthropic")
	if got != "sk-env" {
		t.Errorf("GetProviderAPIKey(anthropic) = %q, want env value %q", got, "sk-env")
	}
}

func TestGetIntegrationToken_FallbackEnv(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	t.Setenv("GITHUB_TOKEN", "ghp-test")

	got := GetIntegrationToken("github")
	if got != "ghp-test" {
		t.Errorf("GetIntegrationToken(github) = %q, want %q", got, "ghp-test")
	}
}

func TestGetIntegrationToken_GHTokenFallback(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	t.Setenv("GH_TOKEN", "gho-test")

	got := GetIntegrationToken("github")
	if got != "gho-test" {
		t.Errorf("GetIntegrationToken(github) = %q, want %q", got, "gho-test")
	}
}

func TestGetIntegrationToken_KeychainFirst(t *testing.T) {
	resetViper(t)
	mk := setupTestKeyring(t)
	t.Setenv("GITHUB_TOKEN", "ghp-env")
	viper.Set("integrations.github.token", "ghp-config")
	mk.Set("agent-minder", "integration/github", "ghp-keychain")

	got := GetIntegrationToken("github")
	if got != "ghp-keychain" {
		t.Errorf("GetIntegrationToken(github) = %q, want keychain value %q", got, "ghp-keychain")
	}
}

func TestSetAndSave(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	// Point Viper at temp dir.
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(filepath.Join(tmpDir, ".agent-minder"))

	SetProviderAPIKey("anthropic", "sk-ant-save-test")
	SetIntegrationToken("github", "ghp-save-test")

	// Save to temp location.
	configDir := filepath.Join(tmpDir, ".agent-minder")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configPath := filepath.Join(configDir, "config.yaml")
	if err := viper.WriteConfigAs(configPath); err != nil {
		t.Fatalf("WriteConfigAs: %v", err)
	}

	// Verify file exists and re-read.
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	resetViper(t)
	setupTestKeyring(t) // re-setup empty keyring after reset
	viper.SetConfigFile(configPath)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("ReadInConfig: %v", err)
	}

	if got := GetProviderAPIKey("anthropic"); got != "sk-ant-save-test" {
		t.Errorf("after reload, anthropic key = %q, want %q", got, "sk-ant-save-test")
	}
	if got := GetIntegrationToken("github"); got != "ghp-save-test" {
		t.Errorf("after reload, github token = %q, want %q", got, "ghp-save-test")
	}
}

func TestConfiguredProviders(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	// Clear any leftover env vars from previous tests.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	viper.Set("providers.anthropic.api_key", "sk-test")

	providers := ConfiguredProviders()
	if len(providers) != 1 || providers[0] != "anthropic" {
		t.Errorf("ConfiguredProviders() = %v, want [anthropic]", providers)
	}
}

func TestConfiguredIntegrations(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	viper.Set("integrations.github.token", "ghp-test")

	integrations := ConfiguredIntegrations()
	if len(integrations) != 1 || integrations[0] != "github" {
		t.Errorf("ConfiguredIntegrations() = %v, want [github]", integrations)
	}
}

func TestHasProvider(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	t.Setenv("ANTHROPIC_API_KEY", "")

	if HasProvider("anthropic") {
		t.Error("HasProvider(anthropic) should be false with no config")
	}

	viper.Set("providers.anthropic.api_key", "sk-test")
	if !HasProvider("anthropic") {
		t.Error("HasProvider(anthropic) should be true after setting key")
	}
}

func TestGetProviderBaseURL(t *testing.T) {
	resetViper(t)
	viper.Set("providers.openai.base_url", "https://api.deepinfra.com/v1")

	got := GetProviderBaseURL("openai")
	if got != "https://api.deepinfra.com/v1" {
		t.Errorf("GetProviderBaseURL(openai) = %q, want deepinfra URL", got)
	}
}

// --- New tests for secure token features ---

func TestTokenSource_Keychain(t *testing.T) {
	resetViper(t)
	mk := setupTestKeyring(t)
	mk.Set("agent-minder", "provider/anthropic", "sk-keychain")

	src := TokenSource("provider", "anthropic")
	if src != "keychain" {
		t.Errorf("TokenSource = %q, want %q", src, "keychain")
	}
}

func TestTokenSource_Env(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")

	src := TokenSource("provider", "anthropic")
	if src != "env" {
		t.Errorf("TokenSource = %q, want %q", src, "env")
	}
}

func TestTokenSource_Config(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	viper.Set("providers.anthropic.api_key", "sk-config")

	src := TokenSource("provider", "anthropic")
	if src != "config" {
		t.Errorf("TokenSource = %q, want %q", src, "config")
	}
}

func TestTokenSource_NotFound(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	t.Setenv("ANTHROPIC_API_KEY", "")

	src := TokenSource("provider", "anthropic")
	if src != "" {
		t.Errorf("TokenSource = %q, want empty", src)
	}
}

func TestTokenSource_Integration(t *testing.T) {
	resetViper(t)
	mk := setupTestKeyring(t)
	mk.Set("agent-minder", "integration/github", "ghp-keychain")

	src := TokenSource("integration", "github")
	if src != "keychain" {
		t.Errorf("TokenSource = %q, want %q", src, "keychain")
	}
}

func TestSetProviderAPIKeySecure_RoundTrip(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	t.Setenv("ANTHROPIC_API_KEY", "")

	if err := SetProviderAPIKeySecure("anthropic", "sk-secure"); err != nil {
		t.Fatalf("SetProviderAPIKeySecure: %v", err)
	}

	got := GetProviderAPIKey("anthropic")
	if got != "sk-secure" {
		t.Errorf("GetProviderAPIKey = %q, want %q", got, "sk-secure")
	}
}

func TestMigrateToKeychain(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)
	t.Setenv("ANTHROPIC_API_KEY", "")

	viper.Set("providers.anthropic.api_key", "sk-migrate-me")

	if err := MigrateToKeychain("provider", "anthropic"); err != nil {
		t.Fatalf("MigrateToKeychain: %v", err)
	}

	// Should be in keychain now.
	if src := TokenSource("provider", "anthropic"); src != "keychain" {
		t.Errorf("after migration, source = %q, want %q", src, "keychain")
	}

	// Should be cleared from config.
	if viper.GetString("providers.anthropic.api_key") != "" {
		t.Error("config file value should be empty after migration")
	}
}

func TestProviderKeyInConfig(t *testing.T) {
	resetViper(t)
	setupTestKeyring(t)

	if ProviderKeyInConfig("anthropic") {
		t.Error("should be false with no config")
	}

	viper.Set("providers.anthropic.api_key", "sk-test")
	if !ProviderKeyInConfig("anthropic") {
		t.Error("should be true after setting key")
	}
}
