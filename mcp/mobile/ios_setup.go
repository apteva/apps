package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Xcode's team cache is only a discovery hint, never a credential. Xcode
// authenticates the build and creates signing assets with automatic signing.
type signingTeam struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Personal bool   `json:"personal"`
}

type iosSetupStatus struct {
	State   string        `json:"state"`
	Message string        `json:"message"`
	Teams   []signingTeam `json:"teams"`
	TeamID  string        `json:"team_id,omitempty"`
}

func xcodeTeams(b []byte) []signingTeam {
	v, err := parsePlist(b)
	if err != nil {
		return nil
	}
	root, _ := v.(map[string]any)
	teams := map[string]signingTeam{}
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			id, _ := x["teamID"].(string)
			if teamIDPattern.MatchString(id) {
				name, _ := x["teamName"].(string)
				personal, _ := x["isFreeProvisioningTeam"].(bool)
				teams[id] = signingTeam{ID: id, Name: name, Personal: personal}
			}
			for _, child := range x {
				visit(child)
			}
		case []any:
			for _, child := range x {
				visit(child)
			}
		}
	}
	// Both cache layouts exist in current Xcode installations. Deduplicate
	// teams but retain all choices rather than guessing between accounts.
	visit(root["IDEProvisioningTeamByIdentifier"])
	visit(root["IDEProvisioningTeams"])
	out := make([]signingTeam, 0, len(teams))
	for _, t := range teams {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (d *drivers) signingTeams(ctx context.Context) []signingTeam {
	b, err := d.command(ctx, "defaults", "export", "com.apple.dt.Xcode", "-")
	if err != nil {
		return nil
	}
	return xcodeTeams(b)
}

func (d *drivers) savedTeam() string {
	if d.cfg.ToolDir == "" {
		return ""
	}
	d.helperMu.Lock()
	defer d.helperMu.Unlock()
	b, err := os.ReadFile(filepath.Join(d.cfg.ToolDir, "ios-team.json"))
	var saved struct {
		TeamID string `json:"team_id"`
	}
	if err != nil || json.Unmarshal(b, &saved) != nil || !teamIDPattern.MatchString(saved.TeamID) {
		return ""
	}
	return saved.TeamID
}

func (d *drivers) selectTeam(team string) error {
	if !teamIDPattern.MatchString(team) {
		return problem("invalid_argument", "select a valid Apple signing team")
	}
	if d.cfg.IOSTeamID != "" && d.cfg.IOSTeamID != team {
		return problem("invalid_configuration", "ios_team_id is fixed in this runner's settings; update it there to change teams")
	}
	if d.cfg.ToolDir == "" {
		return problem("invalid_configuration", "Mobile has no persistent tool directory")
	}
	d.helperMu.Lock()
	defer d.helperMu.Unlock()
	if err := os.MkdirAll(d.cfg.ToolDir, 0700); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]string{"team_id": team})
	f, err := os.CreateTemp(d.cfg.ToolDir, ".ios-team-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(d.cfg.ToolDir, "ios-team.json"))
}

func (d *drivers) iosSetup(ctx context.Context, args map[string]any) (any, error) {
	if !d.nativeIOS() {
		return nil, problem("unsupported", "native iPhone setup runs on the Mac hosting Mobile, with ios_driver=native")
	}
	switch action := str(args, "action"); action {
	case "", "status":
	case "open_xcode":
		if _, err := d.command(ctx, "open", "-a", "Xcode"); err != nil {
			return nil, problem("setup_required", "Install and open Xcode on this Mac, then sign in under Xcode > Settings > Accounts.")
		}
	case "select_team":
		team := str(args, "team_id")
		found := false
		for _, t := range d.signingTeams(ctx) {
			if t.ID == team {
				found = true
			}
		}
		if !found {
			return nil, problem("invalid_argument", "select one of the Apple teams discovered from Xcode, then refresh if you just signed in")
		}
		if err := d.selectTeam(team); err != nil {
			return nil, err
		}
	default:
		return nil, problem("invalid_argument", "setup action must be status, open_xcode or select_team")
	}
	s := iosSetupStatus{Teams: d.signingTeams(ctx)}
	if _, err := d.command(ctx, "xcrun", "--find", "xcodebuild"); err != nil {
		s.State, s.Message = "needs_xcode", "Install Xcode, open it and complete Apple's license and component prompts."
		return s, nil
	}
	team, err := d.teamID(ctx)
	if err != nil {
		if errorCode(err) != "setup_required" {
			return nil, err
		}
		s.State = "needs_account"
		if len(s.Teams) > 1 {
			s.State = "needs_team"
		}
		s.Message = err.Error()
		return s, nil
	}
	s.State, s.TeamID, s.Message = "ready", team, "Signing is configured. Connect and unlock your iPhone, tap Trust and enable Developer Mode if asked. Open a session; Mobile creates certificates and provisioning profiles, builds, signs and installs its runner automatically."
	return s, nil
}

// Free-team profiles expire after a week. Never let a cached build bypass
// automatic provisioning once its embedded profile is missing or expiring.
func (d *drivers) runnerProvisioningValid(ctx context.Context, derived string) bool {
	profiles, _ := filepath.Glob(filepath.Join(derived, "Build", "Products", "*-iphoneos", "*.app", "embedded.mobileprovision"))
	if len(profiles) == 0 {
		return false
	}
	for _, path := range profiles {
		b, err := d.command(ctx, "security", "cms", "-D", "-i", path)
		if err != nil {
			return false
		}
		v, err := parsePlist(b)
		if err != nil {
			return false
		}
		profile, _ := v.(map[string]any)
		expires, _ := profile["ExpirationDate"].(string)
		until, err := time.Parse(time.RFC3339, strings.TrimSpace(expires))
		if err != nil || time.Until(until) < time.Hour {
			return false
		}
	}
	return true
}
