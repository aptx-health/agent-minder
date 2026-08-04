package cmd

import (
	"testing"

	"github.com/aptx-health/agent-minder/internal/coordinator"
	"github.com/aptx-health/agent-minder/internal/scheduler"
)

func TestMilestoneTrigger_Installs(t *testing.T) {
	deploy, store, _ := setupAutomationTest(t)
	cfg, err := scheduler.ParseConfig([]byte(`
jobs:
  milestone-release:
    trigger: "milestone:v2"
    agent: autopilot
`))
	if err != nil {
		t.Fatalf("parse milestone trigger config: %v", err)
	}

	routes := coordinator.TriggerRoutesFromConfig(cfg)
	automations := coordinator.ComputeAutomations(deploy, routes, store, deploy.ID)
	for _, automation := range automations {
		if automation.Kind == coordinator.AutomationTrigger && automation.Expression == "milestone:v2" {
			return
		}
	}

	t.Fatalf("startup summary does not contain active milestone:v2 trigger: %#v", automations)
}
