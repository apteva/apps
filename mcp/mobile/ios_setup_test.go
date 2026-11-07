package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func teamCache(teams string) []byte {
	return []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>IDEProvisioningTeams</key><dict><key>account</key><array>` + teams + `</array></dict></dict></plist>`)
}

const personalTeamXML = `<dict><key>teamID</key><string>AAAAAAAAAA</string><key>teamName</key><string>Personal Team</string><key>isFreeProvisioningTeam</key><true/></dict>`
const otherTeamXML = `<dict><key>teamID</key><string>BBBBBBBBBB</string><key>teamName</key><string>Company</string></dict>`

func TestSigningBootstrapWithoutCertificate(t *testing.T) {
	d := newDrivers(runnerConfig{ToolDir: t.TempDir()})
	d.command = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "defaults" {
			return teamCache(personalTeamXML), nil
		}
		return nil, errors.New("no certificate yet")
	}
	team, err := d.teamID(context.Background())
	if err != nil || team != "AAAAAAAAAA" {
		t.Fatalf("first build cannot bootstrap: %q %v", team, err)
	}
	args := strings.Join(runnerBuildArgs("P", "D", "PHONE", team), " ")
	if !strings.Contains(args, "-allowProvisioningUpdates") || !strings.Contains(args, "DEVELOPMENT_TEAM=AAAAAAAAAA") {
		t.Fatal(args)
	}
}

func TestSigningTeamsRequireExplicitChoiceAndPersist(t *testing.T) {
	d := newDrivers(runnerConfig{ToolDir: t.TempDir()})
	d.command = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "defaults" {
			return teamCache(personalTeamXML + otherTeamXML), nil
		}
		return nil, errors.New("no certificate")
	}
	if _, err := d.teamID(context.Background()); errorCode(err) != "setup_required" || !strings.Contains(err.Error(), "Choose") {
		t.Fatal(err)
	}
	if err := d.selectTeam("BBBBBBBBBB"); err != nil {
		t.Fatal(err)
	}
	other := newDrivers(runnerConfig{ToolDir: d.cfg.ToolDir})
	other.command = d.command
	if got, err := other.teamID(context.Background()); err != nil || got != "BBBBBBBBBB" {
		t.Fatal(got, err)
	}
	info, err := os.Stat(filepath.Join(d.cfg.ToolDir, "ios-team.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	if runtime.GOOS == "darwin" {
		if _, err := d.iosSetup(context.Background(), map[string]any{"action": "select_team", "team_id": "CCCCCCCCCC"}); errorCode(err) != "invalid_argument" {
			t.Fatal("unknown team accepted", err)
		}
	}
}

func TestXcodeTeamCacheLayoutsAndDeduplication(t *testing.T) {
	b := []byte(`<plist><dict><key>IDEProvisioningTeamByIdentifier</key><dict><key>account-id</key><array>` + personalTeamXML + `</array></dict><key>IDEProvisioningTeams</key><dict><key>legacy</key><array>` + personalTeamXML + otherTeamXML + `</array></dict><key>unrelated</key>` + otherTeamXML + `</dict></plist>`)
	got := xcodeTeams(b)
	if len(got) != 2 || got[0].ID != "AAAAAAAAAA" || !got[0].Personal || got[1].Name != "Company" {
		t.Fatalf("teams: %+v", got)
	}
	if len(xcodeTeams([]byte("invalid"))) != 0 {
		t.Fatal("invalid cache accepted")
	}
}

func TestExpiredRunnerProfileRequiresProvisioning(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "Build", "Products", "Debug-iphoneos", "Runner.app", "embedded.mobileprovision")
	if err := os.MkdirAll(filepath.Dir(profile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	d := newDrivers(runnerConfig{})
	for _, offset := range []time.Duration{-time.Hour, 30 * time.Minute, 24 * time.Hour} {
		d.command = func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`<plist><dict><key>ExpirationDate</key><date>` + time.Now().Add(offset).UTC().Format(time.RFC3339) + `</date></dict></plist>`), nil
		}
		if valid := d.runnerProvisioningValid(context.Background(), dir); valid != (offset > time.Hour) {
			t.Fatalf("profile offset %s: valid=%v", offset, valid)
		}
	}
	if err := os.Remove(profile); err != nil {
		t.Fatal(err)
	}
	if d.runnerProvisioningValid(context.Background(), dir) {
		t.Fatal("missing provisioning accepted")
	}
}

func TestConfiguredIPhoneUsesNativeDriver(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native iOS is macOS only")
	}
	for _, driver := range []string{"", `,"driver":"xctest"`} {
		d := newDrivers(runnerConfig{DevicesJSON: `[{"id":"phone","udid":"PHONE","platform":"ios","physical":true` + driver + `}]`})
		d.command = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("offline") }
		devices, _, err := d.devices(context.Background())
		if err != nil || len(devices) != 1 || devices[0].Driver != driverXCTest || devices[0].State != "configured" {
			t.Fatal(devices, err)
		}
	}
}

func TestRunnerPreparationFailureSurvivesPolling(t *testing.T) {
	d := newDrivers(runnerConfig{})
	r := &iosRunner{udid: "PHONE", ready: make(chan struct{}), err: problem("setup_required", "Approve developer trust on the iPhone")}
	close(r.ready)
	d.ios.byUDID = map[string]*iosRunner{"PHONE": r}
	_, err := d.runner(context.Background(), Device{UDID: "PHONE", Physical: true})
	if errorCode(err) != "setup_required" || !strings.Contains(err.Error(), "developer trust") {
		t.Fatal("startup failure lost on retry:", err)
	}
	if len(d.ios.byUDID) != 0 {
		t.Fatal("failed preparation prevents an explicit retry")
	}
	d.stopRunners()
}
