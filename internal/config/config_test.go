package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func resetViper(t *testing.T) {
	t.Helper()
	viper.Reset()
}

func TestGetProviderAPIKey_FromConfig(t *testing.T) {
	resetViper(t)
	viper.Set("providers.anthropic.api_key", "sk-ant-test123")

	got := GetProviderAPIKey("anthropic")
	if got != "sk-ant-test123" {
		t.Errorf("GetProviderAPIKey(anthropic) = %q, want %q", got, "sk-ant-test123")
	}
}

func TestGetProviderAPIKey_FallbackEnv(t *testing.T) {
	resetViper(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-env-fallback")

	got := GetProviderAPIKey("anthropic")
	if got != "sk-env-fallback" {
		t.Errorf("GetProviderAPIKey(anthropic) = %q, want %q", got, "sk-env-fallback")
	}
}

func TestGetProviderAPIKey_ConfigOverridesEnv(t *testing.T) {
	resetViper(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	viper.Set("providers.anthropic.api_key", "sk-config")

	got := GetProviderAPIKey("anthropic")
	if got != "sk-config" {
		t.Errorf("GetProviderAPIKey(anthropic) = %q, want config value %q", got, "sk-config")
	}
}

func TestGetIntegrationToken_FallbackEnv(t *testing.T) {
	resetViper(t)
	t.Setenv("GITHUB_TOKEN", "ghp-test")

	got := GetIntegrationToken("github")
	if got != "ghp-test" {
		t.Errorf("GetIntegrationToken(github) = %q, want %q", got, "ghp-test")
	}
}

func TestGetIntegrationToken_GHTokenFallback(t *testing.T) {
	resetViper(t)
	t.Setenv("GH_TOKEN", "gho-test")

	got := GetIntegrationToken("github")
	if got != "gho-test" {
		t.Errorf("GetIntegrationToken(github) = %q, want %q", got, "gho-test")
	}
}

func TestSetAndSave(t *testing.T) {
	resetViper(t)
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
	viper.Set("integrations.github.token", "ghp-test")

	integrations := ConfiguredIntegrations()
	if len(integrations) != 1 || integrations[0] != "github" {
		t.Errorf("ConfiguredIntegrations() = %v, want [github]", integrations)
	}
}

func TestHasProvider(t *testing.T) {
	resetViper(t)
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
