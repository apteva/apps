package main

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// Lock state is read passively: the credential type comes from
// lock_settings' dump and never from a verify attempt, so reading it cannot
// count as a failed unlock.
const lockScript = `echo @lock; dumpsys lock_settings 2>/dev/null | grep -m1 -E 'CredentialType:'; echo "disabled=$(locksettings get-disabled 2>/dev/null | head -n 1)"; dumpsys window 2>/dev/null | grep -m1 -E 'isKeyguardShowing='; dumpsys power 2>/dev/null | grep -m1 -E 'mWakefulness='`

var (
	reCredential = regexp.MustCompile(`CredentialType:\s*(\w+)`)
	reKeyguard   = regexp.MustCompile(`isKeyguardShowing=(true|false)`)
)

// lockState is what the lock screen looks like right now.
type lockState struct {
	// Keyguard: none (lock screen disabled), swipe (lock screen without a
	// credential) or secure (PIN, pattern or password set); "" when unknown.
	Keyguard string
	Showing  *bool // the lock screen is up
	ScreenOn *bool
}

// parseLockLines reads the @lock section's lines.
func parseLockLines(lines []string) lockState {
	var st lockState
	credential, disabled := "", ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if m := reCredential.FindStringSubmatch(line); m != nil && credential == "" {
			credential = strings.ToUpper(m[1])
		}
		if v, ok := strings.CutPrefix(line, "disabled="); ok {
			disabled = strings.TrimSpace(v)
		}
		if m := reKeyguard.FindStringSubmatch(line); m != nil {
			v := m[1] == "true"
			st.Showing = &v
		}
		if m := reWake.FindStringSubmatch(line); m != nil {
			v := m[1] == "Awake"
			st.ScreenOn = &v
		}
	}
	switch {
	case credential != "" && credential != "NONE":
		st.Keyguard = "secure"
	case credential == "NONE" && disabled == "true":
		st.Keyguard = "none"
	case credential == "NONE":
		st.Keyguard = "swipe"
	}
	return st
}

func parseLockOutput(out string) lockState {
	lines, in := []string{}, false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "@") {
			in = line == "@lock"
			continue
		}
		if in {
			lines = append(lines, line)
		}
	}
	return parseLockLines(lines)
}

func (d *drivers) readLock(ctx context.Context, serial string) (lockState, error) {
	out, err := d.adb(ctx, serial, "shell", lockScript)
	if err != nil {
		return lockState{}, problem("device_unavailable", "could not read the phone's lock state")
	}
	return parseLockOutput(string(out)), nil
}

// wake turns the screen on and dismisses a lock screen that has no
// credential. It never enters a PIN: on a secure lock that is showing it
// refuses (after waking the screen) so a person can unlock the phone.
func (d *drivers) wake(ctx context.Context, serial string) (any, error) {
	before, err := d.readLock(ctx, serial)
	if err != nil {
		return nil, err
	}
	if before.Keyguard == "" {
		return nil, problem("unsupported", "could not tell whether this phone has a PIN; wake refused")
	}
	if _, err := d.shell(ctx, serial, "input", "keyevent", "224"); err != nil {
		return nil, problem("infrastructure_error", "could not wake the screen")
	}
	if before.Keyguard == "secure" && (before.Showing == nil || *before.Showing) {
		return nil, problem("device_locked", "the phone is locked with a PIN, pattern or password; a person must unlock it")
	}
	if before.Keyguard != "secure" {
		_, _ = d.shell(ctx, serial, "wm", "dismiss-keyguard")
	}
	var after lockState
	for i := 0; i < 10; i++ {
		if after, err = d.readLock(ctx, serial); err != nil {
			return nil, err
		}
		if after.Showing != nil && !*after.Showing && after.ScreenOn != nil && *after.ScreenOn {
			break
		}
		if err := sleepCtx(ctx, 200*time.Millisecond); err != nil {
			return nil, err
		}
	}
	unlocked := after.Showing != nil && !*after.Showing && after.ScreenOn != nil && *after.ScreenOn
	if !unlocked {
		return nil, problem("device_locked", "the screen is on but the lock screen did not go away")
	}
	return map[string]any{"ok": true, "screen_on": true, "keyguard": after.Keyguard, "locked": false}, nil
}
