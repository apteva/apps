package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// Diagnostics contain source-space geometry and evidence identities only.
// Temporary source URLs and private pixels must never enter saved parameters.
type smartCropAuditKey struct{}
type smartCropAuditWindow struct {
	W int `json:"w"`
	H int `json:"h"`
	X int `json:"x"`
	Y int `json:"y"`
}

func auditCropWindow(w cropWindow) smartCropAuditWindow {
	return smartCropAuditWindow{W: w.W, H: w.H, X: w.X, Y: w.Y}
}

type smartCropAuditTarget struct {
	FocusMs        int64 `json:"focus_ms"`
	StartMs        int64 `json:"start_ms"`
	EndMs          int64 `json:"end_ms"`
	PreferKeyframe bool  `json:"prefer_keyframe"`
}
type smartCropAuditHead struct {
	Bounds  smartCropAuditWindow `json:"bounds"`
	Quality float32              `json:"detector_quality"`
}

type smartCropEvidence struct {
	AtMs   int64  `json:"at_ms"`
	Origin string `json:"origin"`
	FileID string `json:"file_id,omitempty"`
}
type smartCropExtentEvidence struct {
	ForegroundBounds *smartCropAuditWindow `json:"foreground_bounds,omitempty"`
	UpperPose        *smartCropAuditWindow `json:"upper_pose,omitempty"`
	AtMs             int64                 `json:"at_ms"`
	Bounds           smartCropAuditWindow  `json:"bounds"`
	Head             *smartCropAuditHead   `json:"head,omitempty"`
	Support          string                `json:"support"`
}
type smartCropAudit struct {
	SceneSourceID    string `json:"scene_source_file_id,omitempty"`
	SceneAtMs        *int64 `json:"scene_at_ms,omitempty"`
	SceneRenderID    int64  `json:"scene_render_id,omitempty"`
	mu               sync.Mutex
	AppVersion       string                    `json:"app_version"`
	AlgorithmVersion string                    `json:"algorithm_version"`
	SourceID         string                    `json:"source_file_id"`
	SourceSHA256     string                    `json:"source_sha256,omitempty"`
	SourceWidth      int                       `json:"source_width"`
	SourceHeight     int                       `json:"source_height"`
	SourceRotation   int                       `json:"source_rotation"`
	Requested        smartCropAuditTarget      `json:"requested"`
	Method           string                    `json:"method,omitempty"`
	Evidence         []smartCropEvidence       `json:"evidence"`
	Extents          []smartCropExtentEvidence `json:"subject_extents,omitempty"`
	Fallbacks        []string                  `json:"fallback_reasons,omitempty"`
	Effective        *smartCropAuditWindow     `json:"effective_crop,omitempty"`
	Path             []cropPathPoint           `json:"effective_path,omitempty"`
	Coverage         string                    `json:"action_coverage"`
	Recommendation   string                    `json:"recommendation,omitempty"`
}

func cropAudit(ctx context.Context) *smartCropAudit {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(smartCropAuditKey{}).(*smartCropAudit)
	return a
}
func recordSmartCropEvidence(ctx context.Context, origin string, at int64, fid string) {
	if a := cropAudit(ctx); a != nil {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.Evidence = append(a.Evidence, smartCropEvidence{at, origin, fid})
	}
}
func recordSmartCropFallback(ctx context.Context, reason string) {
	if a := cropAudit(ctx); a != nil {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.Fallbacks = append(a.Fallbacks, reason)
	}
}
func recordSmartCropMethod(ctx context.Context, method string) {
	if a := cropAudit(ctx); a != nil {
		a.Method = method
	}
}
func recordSmartCropExtent(ctx context.Context, sample smartCropV2Sample, extent *smartCropSubjectExtent, cropW int) {
	if a := cropAudit(ctx); a != nil && extent != nil {
		var head *smartCropAuditHead
		if h := extent.Head; h != nil {
			head = &smartCropAuditHead{Bounds: auditCropWindow(cropWindow{X: h.MinX, Y: h.MinY, W: h.MaxX - h.MinX, H: h.MaxY - h.MinY}), Quality: h.Quality}
		}
		var upper *smartCropAuditWindow
		if extent.UpperPose != nil {
			v := auditCropWindow(*extent.UpperPose)
			upper = &v
		}
		var raw *smartCropAuditWindow
		if extent.ForegroundBounds != nil {
			v := auditCropWindow(*extent.ForegroundBounds)
			raw = &v
		}
		a.Extents = append(a.Extents, smartCropExtentEvidence{ForegroundBounds: raw, AtMs: sample.point.AtMs, Bounds: auditCropWindow(extent.Bounds), Head: head, Support: extent.Evidence, UpperPose: upper})
		if extent.Bounds.W > cropW {
			a.Coverage = "exceeds_crop_width"
			a.Recommendation = "Use fit_mode: contain to preserve the full source frame."
		} else if a.Coverage == "unknown" {
			a.Coverage = "sampled_extent_fits"
		}
	}
}
func attachSmartCropAudit(out []byte, a *smartCropAudit) []byte {
	var p map[string]any
	if json.Unmarshal(out, &p) != nil {
		return out
	}
	if w := int(int64FromJSONValue(p["crop_w"])); w > 0 {
		a.Effective = &smartCropAuditWindow{W: w, H: int(int64FromJSONValue(p["crop_h"])), X: int(int64FromJSONValue(p["crop_x"])), Y: int(int64FromJSONValue(p["crop_y"]))}
	}
	if path, ok := p["crop_path"]; ok {
		raw, _ := json.Marshal(path)
		_ = json.Unmarshal(raw, &a.Path)
	}
	sort.Slice(a.Evidence, func(i, j int) bool {
		l, r := a.Evidence[i], a.Evidence[j]
		if l.Origin != r.Origin {
			return l.Origin < r.Origin
		}
		if l.AtMs != r.AtMs {
			return l.AtMs < r.AtMs
		}
		return l.FileID < r.FileID
	})
	evidence := make([]smartCropEvidence, 0, len(a.Evidence))
	for _, e := range a.Evidence {
		if len(evidence) == 0 || e != evidence[len(evidence)-1] {
			evidence = append(evidence, e)
		}
	}
	a.Evidence = evidence
	sort.Strings(a.Fallbacks)
	reasons := make([]string, 0, len(a.Fallbacks))
	for _, r := range a.Fallbacks {
		if len(reasons) == 0 || r != reasons[len(reasons)-1] {
			reasons = append(reasons, r)
		}
	}
	a.Fallbacks = reasons
	p["crop_diagnostics"] = a
	raw, err := json.Marshal(p)
	if err != nil {
		return out
	}
	return raw
}

// Stable reason codes retain the failure category without persisting transient
// FFmpeg stderr, local paths, credentials or signed source URLs.
func smartCropFailureReason(err error) string {
	if errors.Is(err, context.Canceled) {
		return "analysis_cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "analysis_timeout"
	}
	if err == nil {
		return "analysis_unavailable"
	}
	s := err.Error()

	for _, reason := range []struct{ text, code string }{
		{"source dimensions unavailable", "source_dimensions_unavailable"},
		{"get source", "source_metadata_unavailable"},
		{"no usable frame derivation", "no_usable_derivation"},
		{"no decodable frame derivation", "no_decodable_derivation"},
		{"sample source", "source_sampling_unavailable"},
		{"fewer than two usable samples", "insufficient_reel_samples"},
		{"empty crop path", "empty_crop_path"},
	} {
		if strings.Contains(s, reason.text) {
			return reason.code
		}
	}

	return "analysis_unavailable"
}
