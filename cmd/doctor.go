package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/aptx-health/agent-minder/internal/runtime"
	"github.com/aptx-health/agent-minder/internal/scheduler"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check local agent runtime capabilities",
	RunE:  runDoctor,
}

var flagDoctorRepo string

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().StringVar(&flagDoctorRepo, "repo", ".", "Repository directory")
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	repoDir, err := resolveRepoDir(flagDoctorRepo)
	if err != nil {
		repoDir = flagDoctorRepo
	}
	return doctorCheck(cmd.Context(), repoDir)
}

func doctorCheck(ctx context.Context, repoDir string) error {
	fmt.Println("Runtime capabilities:")
	fmt.Printf("%-12s %-10s %-24s %-24s %s\n", "RUNTIME", "CLI", "VERSION", "FEATURES", "MODELS")
	fmt.Printf("%-12s %-10s %-24s %-24s %s\n", "-------", "---", "-------", "--------", "------")

	for _, name := range []string{runtime.NameClaudeCode, runtime.NameCodex, runtime.NameOpenCode} {
		rt, err := runtime.Lookup(name)
		if err != nil {
			fmt.Printf("%-12s %-10s %-24s %-24s %s\n", name, "unknown", "-", "-", err.Error())
			continue
		}
		caps, err := runtime.CachedCapabilities(ctx, rt)
		status := "ok"
		version := caps.Version
		if version == "" {
			version = "-"
		}
		if err != nil {
			status = "error"
			version = err.Error()
		} else if !caps.Available {
			status = "missing"
			version = caps.Error
			if version == "" {
				version = "not available"
			}
		}
		fmt.Printf("%-12s %-10s %-24s %-24s %s\n",
			name, status, truncateStr(version, 24), featureSummary(caps.Features), runtime.ModelSupportSummary(caps))
	}

	failures, err := doctorCheckJobs(ctx, repoDir)
	if err != nil {
		return err
	}
	if len(failures) == 0 {
		return nil
	}
	fmt.Println()
	fmt.Println("jobs.yaml problems:")
	for _, failure := range failures {
		fmt.Println("  - " + failure)
	}
	return fmt.Errorf("%d jobs.yaml entr%s cannot run", len(failures), pluralY(len(failures)))
}

func doctorCheckJobs(ctx context.Context, repoDir string) ([]string, error) {
	cfgPath := scheduler.ConfigPath(repoDir)
	if _, err := os.Stat(cfgPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat jobs.yaml: %w", err)
	}
	cfg, err := scheduler.LoadConfig(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load jobs.yaml: %w", err)
	}
	defaultRuntime, err := runtime.Resolve("", repoDir)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(cfg.Jobs))
	for name := range cfg.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)

	var failures []string
	for _, name := range names {
		def := cfg.Jobs[name]
		runtimeName := def.Runtime
		if runtimeName == "" {
			runtimeName = defaultRuntime.Name
		}
		rt, err := runtime.Lookup(runtimeName)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if err := runtime.Preflight(ctx, rt, def.Model); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
		}
	}
	return failures, nil
}

func featureSummary(features runtime.RuntimeFeatures) string {
	var out []string
	if features.StructuredOutput {
		out = append(out, "structured")
	}
	if features.BudgetFlag {
		out = append(out, "budget")
	}
	if features.SessionResume {
		out = append(out, "resume")
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, ",")
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
