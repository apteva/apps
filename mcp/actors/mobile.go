package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Mobile reuses device app data, without isolated browser profiles or snapshots.
type actorSessionTarget struct {
	Kind     string `json:"kind"`
	DeviceID string `json:"device_id,omitempty"`
	Platform string `json:"platform,omitempty"`
	Physical *bool  `json:"physical,omitempty"`
	Mode     string `json:"mode,omitempty"`
	AppID    string `json:"app_id,omitempty"`
}
type actorCapabilities struct {
	TargetKind      string   `json:"target_kind"`
	CoordinateSpace string   `json:"coordinate_space"`
	Actions         []string `json:"actions"`
	Keys            []string `json:"keys"`
	Observations    []string `json:"observations"`
}

func screenInputAction(action string) bool {
	switch action {
	case "tap", "swipe", "home", "launch_app", "stop_app", "native_tap", "native_tap_badged", "native_tap_if_present", "native_type", "native_replace", "native_clear", "native_type_secret", "native_type_private":
		return true
	}
	return false
}
func screenCoordinate(v any, templates bool) (int, error) {
	if templates && actorTemplateValue(stringFromAny(v)) {
		return 0, nil
	}
	n, ok := actorNumber(v)
	if !ok || n < 0 || n > 10000 || n != math.Trunc(n) {
		return 0, errors.New("screen coordinates must be integers between 0 and 10000")
	}
	return int(n), nil
}
func validateScreenStep(step actorStep, templates bool) error {
	if len(step.NativeValues) > 12 {
		return errors.New("at most 12 native value guards are supported")
	}
	for key, value := range step.NativeValues {
		if key == "" || len(key) > 500 || len(value) > 4096 {
			return errors.New("invalid native field value guard")
		}
	}
	if step.Action == "native_read" && (step.VerifiedField == "" || !operationNamePattern.MatchString(step.VerifiedField) || step.Text != "") {
		return errors.New("native_read requires verified_field and no input text")
	}
	if step.Action == "native_clear" && step.Text != "" {
		return errors.New("native_clear does not accept input text")
	}
	keypadSecret := step.Action == "native_type_private" && step.Mode == "keypad"
	switch step.Action {
	case "native_read", "native_assert", "native_wait", "native_tap", "native_tap_badged", "native_tap_if_present", "native_type", "native_replace", "native_clear", "native_type_secret", "native_type_private":
		if strings.TrimSpace(step.NativeApp) == "" || len(step.NativeRequired) == 0 || len(step.NativeRequired) > 12 {
			return errors.New("native screen requires native_app and 1–12 native_required markers")
		}
		for _, marker := range step.NativeRequired {
			if strings.TrimSpace(marker) == "" || len(marker) > 500 {
				return errors.New("native screen marker must contain 1–500 bytes")
			}
		}
		if step.Action != "native_assert" && step.Action != "native_wait" && !keypadSecret && strings.TrimSpace(step.NativeTarget) == "" {
			return errors.New("native input requires an exact native_target")
		}
		if step.Action == "native_type_private" && step.Mode != "" && (!keypadSecret || step.NativeTarget != "") {
			return errors.New("private input mode must be keypad with screen markers and no native_target")
		}
		if (step.Action == "native_type" || step.Action == "native_replace") && (step.Text == "" || len(step.Text) > 256 || strings.ContainsAny(step.Text, "\r\n\t\b")) {
			return errors.New("native text input requires 1–256 bytes of single-line text")
		}
		if step.Action == "native_wait" {
			n, ok := actorNumber(step.Duration)
			if !ok || n < 1 || n > 28000 || n != math.Trunc(n) || step.Text != "" || step.NativeTarget != "" {
				return errors.New("native_wait requires 1–28000 duration_ms and screen markers only")
			}
		}
		if nativeSecretAction(step.Action) && (strings.TrimSpace(step.SecretRef) == "" || step.Text != "" || step.Value != "" || step.Optional) {
			return errors.New("native secret input requires secret_ref and disallows literal input or optional")
		}
	}

	if step.Action == "tap" || step.Action == "swipe" {
		values := []any{step.X, step.Y}
		if step.Action == "swipe" {
			values = append(values, step.ToX, step.ToY)
		}
		for _, v := range values {
			if _, err := screenCoordinate(v, templates); err != nil {
				return err
			}
		}
	}
	if (step.Action == "launch_app" || step.Action == "stop_app") && strings.TrimSpace(step.AppID) == "" {
		return errors.New("app_id is required")
	}
	return nil
}
func validateMobileActor(def actorDefinition, templates bool) error {
	b := def.Browser
	if b.Backend != "mobile" {
		if b.Target != nil || b.DeviceID != "" {
			return errors.New("browser.target and device_id require backend=mobile")
		}
		for _, s := range def.Steps {
			if s.Action == "open" || s.Action == "native_read" || s.Action == "native_assert" || s.Action == "native_wait" || screenInputAction(s.Action) {
				return errors.New("screen actions require backend=mobile")
			}
		}
		return nil
	}
	if def.Crawl != nil {
		return errors.New("Mobile does not support DOM crawls")
	}
	if len(b.Viewport) > 0 || len(b.Environment) > 0 || b.ProxyMode != "" || b.ProxyProfile != "" || b.ProxyCountry != "" || b.ProxySticky != "" {
		return errors.New("Mobile does not support browser viewport, environment or proxy settings")
	}
	if b.ContextID == "" && b.DeviceID == "" && (b.Target == nil || b.Target.DeviceID == "") {
		return errors.New("Mobile requires context_id, device_id or target.device_id")
	}
	if b.Target != nil {
		if b.Target.Kind != "mobile" {
			return errors.New("Mobile target.kind must be mobile")
		}
		if b.Target.Mode != "" && b.Target.Mode != "screen" {
			return errors.New("Mobile actor target.mode must be screen")
		}
		if b.Target.AppID != "" {
			return errors.New("use a launch_app step instead of target.app_id so capabilities are checked before input")
		}
		if b.DeviceID != "" && b.Target.DeviceID != "" && b.DeviceID != b.Target.DeviceID {
			return errors.New("device_id and target.device_id must match")
		}
	}
	for i, s := range def.Steps {
		if (s.Action == "goto" || s.Action == "open") && s.Optional {
			return errors.New("Mobile session opening and capability checks cannot be optional")
		}
		switch s.Action {
		case "open", "goto", "tap", "swipe", "home", "launch_app", "stop_app", "type", "key", "scroll", "wait", "screenshot", "assert_inputs", "native_read", "native_assert", "native_wait", "native_tap", "native_tap_badged", "native_tap_if_present", "native_type", "native_replace", "native_clear":
		case "native_type_secret", "native_type_private":
			if !def.PrivateScreen || strings.TrimSpace(b.ContextID) == "" {
				return errors.New("native secret input requires private_screen and a saved context_id")
			}
		default:
			return fmt.Errorf("steps[%d]: Mobile does not support %s; DOM, SoM, form controls and uploads are unavailable", i, s.Action)
		}
		if locatorHasTarget(s.Locator) || s.TargetID != "" || s.ExpectedName != "" || s.ExpectedRole != "" || s.ExpectedText != "" || s.ExpectedEffect != "" || s.EffectPhase != "" || s.ConfirmConsequence != "" || s.OnceKey != "" || s.ReadViews != nil || s.URLFromField != "" {
			return fmt.Errorf("steps[%d]: Mobile does not support semantic targets or DOM/consequence guards", i)
		}
		if err := validateScreenStep(s, templates); err != nil {
			return fmt.Errorf("steps[%d]: %w", i, err)
		}
	}
	return nil
}
func resolveMobileBrowser(callCtx context.Context, ctx *sdk.AppCtx, def *actorDefinition) error {
	callCtx, cancel := context.WithTimeout(callCtx, 10*time.Second)
	defer cancel()
	b := &def.Browser
	b.ContextID = strings.TrimSpace(b.ContextID)
	b.DeviceID = strings.TrimSpace(b.DeviceID)
	if b.Target != nil {
		b.Target.DeviceID = strings.TrimSpace(b.Target.DeviceID)
	}
	if b.ContextID != "" {
		var out struct {
			Found   bool `json:"found"`
			Context struct {
				Backend string              `json:"backend"`
				Target  *actorSessionTarget `json:"target"`
			} `json:"context"`
		}
		if err := sdk.CallAppResultContext(callCtx, ctx.PlatformAPI(), "computer", "computer_context_get", withProjectID(ctx, map[string]any{"id": b.ContextID}), &out); err != nil {
			return fmt.Errorf("resolve Computer context: %w", err)
		}
		if !out.Found {
			return errors.New("Computer context not found in this project")
		}
		if out.Context.Backend == "mobile" {
			if b.Backend != "" && b.Backend != "mobile" {
				return errors.New("saved Mobile context conflicts with requested backend")
			}
			saved := out.Context.Target
			if saved == nil || saved.DeviceID == "" {
				return errors.New("saved Mobile context has no device target")
			}
			if (b.DeviceID != "" && b.DeviceID != saved.DeviceID) || (b.Target != nil && (b.Target.DeviceID != saved.DeviceID || (b.Target.Platform != "" && b.Target.Platform != saved.Platform) || (b.Target.Physical != nil && (saved.Physical == nil || *b.Target.Physical != *saved.Physical)))) {
				return errors.New("requested Mobile target conflicts with saved context")
			}
			b.Backend = "mobile"
			b.Target = saved
		} else if b.Backend == "mobile" {
			return errors.New("requested Mobile backend conflicts with saved browser context")
		}
	}
	if b.Backend == "mobile" && b.Target == nil {
		b.Target = &actorSessionTarget{Kind: "mobile", DeviceID: b.DeviceID, Mode: "screen"}
	}
	if b.Backend == "mobile" && b.Target.DeviceID == "" {
		b.Target.DeviceID = b.DeviceID
	}
	if def.SelectedPlatform != "" {
		platform, err := actorBrowserPlatform(*b)
		if err != nil {
			return err
		}
		if platform != def.SelectedPlatform {
			return errors.New("saved device/context platform changed from the pinned platform variant")
		}
	}
	return validateMobileActor(*def, false)
}
func mobileQueueKey(b actorBrowser) string {
	if b.Backend != "mobile" {
		return ""
	}
	id := b.DeviceID
	if b.Target != nil && b.Target.DeviceID != "" {
		id = b.Target.DeviceID
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	return "mobile-device:" + id
}
func resolveActorQueueKey(ctx *sdk.AppCtx, run *actorQueuedRun) (string, error) {
	def, _, _, err := renderQueuedActor(run)
	if err != nil {
		return actorQueueKey(run), nil
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := resolveMobileBrowser(callCtx, ctx, &def); err != nil {
		return "", err
	}
	if key := mobileQueueKey(def.Browser); key != "" {
		return key, nil
	}
	return actorQueueKey(run), nil
}
func (e *actorExecution) lockMobileDevice() error {
	key := mobileQueueKey(e.definition.Browser)
	res, err := e.ctx.AppDB().Exec(`INSERT INTO actors_context_locks(project_id,context_id,run_id,expires_at) VALUES(?,?,?,?) ON CONFLICT(project_id,context_id) DO UPDATE SET run_id=excluded.run_id,expires_at=excluded.expires_at,cleanup_pending=0 WHERE actors_context_locks.expires_at<=? OR actors_context_locks.run_id=excluded.run_id`, projectID(e.ctx), key, e.run.ID, e.deadline.Add(time.Minute).Unix(), time.Now().Unix())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("Mobile device is reserved by another run")
	}
	return nil
}
func mobileComputerAction(step actorStep) string {
	switch step.Action {
	case "open":
		return ""
	case "goto":
		return "navigate"
	case "tap":
		return "click"
	case "assert_inputs":
		return ""
	case "native_type_secret":
		return "native_type_secure"
	case "native_type_private":
		if step.Mode == "keypad" {
			return "native_tap"
		}
		return "native_type"
	}
	return step.Action
}
func hasCapability(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func validateMobileCapabilities(def actorDefinition, session *browserSession) error {
	c := session.Capabilities
	if session.Backend != "mobile" || c == nil || c.TargetKind != "mobile" || c.CoordinateSpace != "screen_pixels" || !hasCapability(c.Observations, "screenshot") {
		return errors.New("Computer 0.8.0+ must report Mobile screen capabilities")
	}
	if session.Target == nil || session.Target.DeviceID != def.Browser.Target.DeviceID {
		return errors.New("Computer opened an unexpected Mobile device")
	}
	for _, s := range def.Steps {
		if action := mobileComputerAction(s); action != "" && !hasCapability(c.Actions, action) {
			return fmt.Errorf("Mobile does not support action %s", s.Action)
		}
		if s.Action == "key" && !hasCapability(c.Keys, s.Key) {
			return fmt.Errorf("Mobile does not support key %q", s.Key)
		}
	}
	return nil
}
func (e *actorExecution) screenInteract(step actorStep) error {
	if e.session == nil || e.session.Backend != "mobile" {
		return errors.New("screen input requires an open Mobile session")
	}
	if err := validateScreenStep(step, false); err != nil {
		return err
	}
	if step.Action == "native_type_private" && step.Mode == "keypad" {
		return e.enterNativeSecretOnKeypad(step)
	}
	args := map[string]any{"session_id": e.session.SessionID, "action": mobileComputerAction(step)}
	if step.Action == "tap" || step.Action == "swipe" {
		x, _ := screenCoordinate(step.X, false)
		y, _ := screenCoordinate(step.Y, false)
		args["coordinate"] = fmt.Sprintf("%d,%d", x, y)
		if step.Action == "swipe" {
			x, _ = screenCoordinate(step.ToX, false)
			y, _ = screenCoordinate(step.ToY, false)
			args["to_x"], args["to_y"] = x, y
		}
	}
	if step.Action == "native_read" || step.Action == "native_assert" || step.Action == "native_wait" || step.Action == "native_tap" || step.Action == "native_tap_badged" || step.Action == "native_tap_if_present" || step.Action == "native_type" || step.Action == "native_replace" || step.Action == "native_clear" || nativeSecretAction(step.Action) {
		args["native_app"] = step.NativeApp
		args["native_required"] = step.NativeRequired
		if len(step.NativeValues) > 0 {
			args["native_values"] = step.NativeValues
		}
		if step.Action == "native_wait" {
			args["duration"] = templateInt(step.Duration)
		}
		if step.NativeTarget != "" {
			args["native_target"] = step.NativeTarget
		}
		if step.Text != "" {
			args["text"] = step.Text
		}
		if nativeSecretAction(step.Action) {
			value, err := e.nativeSecret(step)
			if err != nil {
				return err
			}
			if e.definition.SelectedPlatform == "android" || (e.session.Target != nil && e.session.Target.Platform == "android") {
				args["password"] = value
			} else {
				args["text"] = value
			}
			args["observation"] = "none"
		}
	}
	if step.AppID != "" {
		args["app_id"] = step.AppID
	}
	var out map[string]any
	if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, args), &out); err != nil {
		if nativeSecretAction(step.Action) {
			return errors.New("secret-backed native input failed; inspect the screen privately")
		}
		return err
	}
	if nativeSecretAction(step.Action) && out["success"] != true {
		return errors.New("secret-backed native input was not confirmed")
	}
	if err := finishScreenInteraction(out); err != nil {
		return err
	}
	if step.Action == "native_read" {
		return e.recordNativeRead(step, out)
	}
	return nil
}

func (e *actorExecution) enterNativeSecretOnKeypad(step actorStep) error {
	value, err := e.nativeSecret(step)
	if err != nil {
		return err
	}
	if len(value) > 12 || strings.Trim(value, "0123456789") != "" {
		return errors.New("private keypad input requires 1–12 digits")
	}
	for _, digit := range value {
		if err := e.checkpoint(); err != nil {
			return err
		}
		args := map[string]any{
			"session_id": e.session.SessionID, "action": "native_tap",
			"native_app": step.NativeApp, "native_required": step.NativeRequired,
			"native_target": string(digit), "observation": "none",
		}
		var out map[string]any
		if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, args), &out); err != nil {
			return errors.New("private keypad input failed; inspect the screen privately")
		}
		if err := finishScreenInteraction(out); err != nil {
			return err
		}
	}
	return nil
}

func finishScreenInteraction(out map[string]any) error {
	if success, ok := out["success"].(bool); ok && !success {
		return errors.New("Mobile input reported an unsuccessful outcome; inspect the screen before retrying")
	}
	return nil
}
