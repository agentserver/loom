package driver

import (
	"log"
	"sync"

	"github.com/yourorg/multi-agent/internal/ablation"
)

// NoUserPromotionPath ablation target — the FlagName is declared in
// internal/ablation (Phase 1 WT-1-ablation-registry); this package
// owns the *bool target. Spec §5.

var (
	noUserPromotionPath         bool
	noUserPromotionPathInitErr  error
	noUserPromotionPathWarnOnce sync.Once
)

// IsNoUserPromotionPath reports whether the NoUserPromotionPath
// ablation is active. Read by (a) SurfacePromoteCandidate, (b)
// promotion_pipeline_tool via Deps.IsPromotionPathDisabled, (c)
// registerSlaveMCPTool.Call for the driver-inferred gate.
func IsNoUserPromotionPath() bool { return noUserPromotionPath }

func init() {
	if err := ablation.Default.Register(ablation.NoUserPromotionPath, &noUserPromotionPath); err != nil {
		noUserPromotionPathInitErr = err
		log.Printf("driver: ablation.Default.Register(NoUserPromotionPath) failed: %v — --ablation NoUserPromotionPath will not gate SurfacePromoteCandidate, promotion_pipeline, or register_slave_mcp", err)
	}
}

// surfacePromotionInitErrorOnce is invoked by
// SurfacePromoteCandidate, promotion_pipeline_tool, and
// registerSlaveMCPTool on their first entry. If registration failed
// at init, emit ONE ERROR log line so it's visible even when stderr
// was suppressed at startup — matches B4's NoRegistryLookup
// pattern.
func surfacePromotionInitErrorOnce() {
	if noUserPromotionPathInitErr == nil {
		return
	}
	noUserPromotionPathWarnOnce.Do(func() {
		log.Printf("[error] NoUserPromotionPath ablation wiring inert (init err: %v) — driver-initiated promotion paths (SurfacePromoteCandidate, promotion_pipeline, register_slave_mcp) will run unguarded", noUserPromotionPathInitErr)
	})
}

// resetNoUserPromotionPathForTest — test-only.
func resetNoUserPromotionPathForTest() {
	noUserPromotionPath = false
	noUserPromotionPathInitErr = nil
	noUserPromotionPathWarnOnce = sync.Once{}
}
