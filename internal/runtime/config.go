package runtime

import (
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

const ConfigFileName = "config.yaml"

// Resolution describes the runtime selected after applying the fallback
// hierarchy.
type Resolution struct {
	Name   string
	Source string
}

type configFile struct {
	Runtime string `yaml:"runtime"`
}

// Resolve applies the deployment-level runtime hierarchy:
// explicit CLI flag, repo config, user config, built-in default.
func Resolve(explicit, repoDir string) (Resolution, error) {
	if explicit != "" {
		return validateResolution(explicit, "flag")
	}
	if repoDir != "" {
		name, ok, err := readRuntimeConfig(filepath.Join(repoDir, ".agent-minder", ConfigFileName))
		if err != nil {
			return Resolution{}, err
		}
		if ok {
			return validateResolution(name, "repo")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		name, ok, err := readRuntimeConfig(filepath.Join(home, ".agent-minder", ConfigFileName))
		if err != nil {
			return Resolution{}, err
		}
		if ok {
			return validateResolution(name, "user")
		}
	}
	return validateResolution(DefaultName, "default")
}

func validateResolution(name, source string) (Resolution, error) {
	if err := Validate(name); err != nil {
		return Resolution{}, fmt.Errorf("%s runtime config: %w", source, err)
	}
	return Resolution{Name: name, Source: source}, nil
}

func readRuntimeConfig(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read runtime config %s: %w", path, err)
	}
	var cfg configFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", false, fmt.Errorf("parse runtime config %s: %w", path, err)
	}
	if cfg.Runtime == "" {
		return "", false, nil
	}
	return cfg.Runtime, true, nil
}
