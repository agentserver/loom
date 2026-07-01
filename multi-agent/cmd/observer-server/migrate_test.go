package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNeedsCommanderDDL_TelemetryClusterOnly verifies all 8 combinations of
// (commander/agentserver URL, telemetry, cluster, postgres) for needsCommanderDDL.
// The table also documents the "telemetry-only cluster" scenario: a pod running
// without a commander URL still needs DDL when the PG telemetry limiter is
// selected (telemetry + cluster + postgres) — this was the r1 bug.
func TestNeedsCommanderDDL_TelemetryClusterOnly(t *testing.T) {
	cases := []struct {
		name           string
		agentserverURL string // empty = commander disabled
		telemetry      bool
		cluster        bool
		driver         string
		wantNeeds      bool
	}{
		// Commander enabled always needs DDL regardless of telemetry/cluster/driver.
		{name: "commander_enabled_sqlite", agentserverURL: "https://as.example.com", telemetry: false, cluster: false, driver: "sqlite", wantNeeds: true},
		{name: "commander_enabled_postgres", agentserverURL: "https://as.example.com", telemetry: true, cluster: true, driver: "postgres", wantNeeds: true},

		// Telemetry+cluster+postgres → needs DDL for commander_telemetry_buckets.
		{name: "telemetry_cluster_postgres", agentserverURL: "", telemetry: true, cluster: true, driver: "postgres", wantNeeds: true},

		// Partial combinations of telemetry/cluster/postgres → no DDL needed.
		{name: "telemetry_cluster_sqlite", agentserverURL: "", telemetry: true, cluster: true, driver: "sqlite", wantNeeds: false},
		{name: "telemetry_no_cluster_postgres", agentserverURL: "", telemetry: true, cluster: false, driver: "postgres", wantNeeds: false},
		{name: "no_telemetry_cluster_postgres", agentserverURL: "", telemetry: false, cluster: true, driver: "postgres", wantNeeds: false},

		// Plain single-pod telemetry (no cluster, no commander).
		{name: "telemetry_only_sqlite", agentserverURL: "", telemetry: true, cluster: false, driver: "sqlite", wantNeeds: false},

		// Nothing special: base case with no enablement.
		{name: "none_sqlite", agentserverURL: "", telemetry: false, cluster: false, driver: "sqlite", wantNeeds: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Identity: IdentityConfig{
					Agentserver: AgentserverIdentityConfig{
						URL: tc.agentserverURL,
					},
				},
				Telemetry: TelemetryConfig{Enabled: tc.telemetry},
				Cluster:   ClusterConfig{Enabled: tc.cluster},
				Store:     StoreConfig{Driver: tc.driver},
			}
			got := needsCommanderDDL(cfg)
			require.Equal(t, tc.wantNeeds, got,
				"needsCommanderDDL mismatch for %s: agentserverURL=%q telemetry=%v cluster=%v driver=%s",
				tc.name, tc.agentserverURL, tc.telemetry, tc.cluster, tc.driver)
		})
	}
}
