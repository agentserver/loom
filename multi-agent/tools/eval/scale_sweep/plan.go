// Package scale_sweep expands the §E5 scale axes into a stable list of
// ScalePoints. The sweep harness under sweep/ runs each point through
// the three microbench binaries and reads probe_events + route_reasons
// for the two remaining metrics.
package scale_sweep

import "sort"

// ScaleAxes captures the three-axis cross product from paper §E5 "Scale
// points" (08_evaluation_plan_v3.md lines 213–217). The Default* zero
// values ARE the paper's axes; overriding is only for CI smoke or
// operator experimentation.
type ScaleAxes struct {
	Contexts        []int   // paper default: {1, 2, 4, 8, 16}
	ToolsPerContext []int   // paper default: {10, 50, 100}
	ArtifactSizes   []int64 // paper default: {1<<10, 1<<20, 100<<20}
}

// DefaultAxes returns the §E5 axes verbatim. Kept as a distinct function
// so tests can assert `ScaleAxes{}` (zero value) produces the same axes
// as DefaultAxes() — i.e. Expand({}) must not accidentally return an
// empty list.
func DefaultAxes() ScaleAxes {
	return ScaleAxes{
		Contexts:        []int{1, 2, 4, 8, 16},
		ToolsPerContext: []int{10, 50, 100},
		ArtifactSizes:   []int64{1 << 10, 1 << 20, 100 << 20},
	}
}

// ScalePoint is one row of the sweep matrix.
type ScalePoint struct {
	Contexts          int
	ToolsPerContext   int
	ArtifactSizeBytes int64
}

// Expand returns the cross-product of the three axes, lexicographically
// sorted (contexts asc, then tools_per_context asc, then
// artifact_size_bytes asc) so CSV output order is stable across runs.
// Empty axes fall back to DefaultAxes so `Expand(ScaleAxes{})` yields
// the paper's 45-point matrix.
func Expand(a ScaleAxes) []ScalePoint {
	if len(a.Contexts) == 0 {
		a.Contexts = DefaultAxes().Contexts
	}
	if len(a.ToolsPerContext) == 0 {
		a.ToolsPerContext = DefaultAxes().ToolsPerContext
	}
	if len(a.ArtifactSizes) == 0 {
		a.ArtifactSizes = DefaultAxes().ArtifactSizes
	}
	pts := make([]ScalePoint, 0, len(a.Contexts)*len(a.ToolsPerContext)*len(a.ArtifactSizes))
	for _, c := range a.Contexts {
		for _, t := range a.ToolsPerContext {
			for _, sz := range a.ArtifactSizes {
				pts = append(pts, ScalePoint{c, t, sz})
			}
		}
	}
	sort.Slice(pts, func(i, j int) bool {
		if pts[i].Contexts != pts[j].Contexts {
			return pts[i].Contexts < pts[j].Contexts
		}
		if pts[i].ToolsPerContext != pts[j].ToolsPerContext {
			return pts[i].ToolsPerContext < pts[j].ToolsPerContext
		}
		return pts[i].ArtifactSizeBytes < pts[j].ArtifactSizeBytes
	})
	return pts
}
