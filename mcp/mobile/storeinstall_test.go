package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

const playListing = `<?xml version="1.0"?><hierarchy>
<node text="" class="android.widget.FrameLayout" bounds="[0,0][1080,2400]">
<node text="Example Game" class="android.widget.TextView" bounds="[40,300][700,380]"/>
<node text="Install" class="android.widget.Button" clickable="true" bounds="[600,900][1000,1000]"/>
</node></hierarchy>`

const testflightList = `<?xml version="1.0"?><AppiumAOSElement>
<XCUIElementTypeApplication name="TestFlight" x="0" y="0" width="402" height="874" visible="true">
<XCUIElementTypeStaticText name="Hidden Game" label="Hidden Game" x="10" y="10" width="100" height="20" visible="false"/>
<XCUIElementTypeCell name="Example Game" label="Example Game" x="0" y="200" width="402" height="80" visible="true"/>
<XCUIElementTypeButton name="INSTALL" label="INSTALL" x="300" y="220" width="80" height="30" visible="true"/>
</XCUIElementTypeApplication></AppiumAOSElement>`

func TestFindElementsAcrossPlatforms(t *testing.T) {
	got := findElements(playListing, installButtons)
	if len(got) != 1 || got[0].cx != 800 || got[0].cy != 950 {
		t.Fatalf("android: %+v", got)
	}
	got = findElements(testflightList, []matcher{{Text: "Hidden Game"}})
	if len(got) != 0 {
		t.Fatal("matched an invisible element")
	}
	got = findElements(testflightList, installButtons)
	if len(got) == 0 || got[0].cx != 340 || got[0].cy != 235 {
		t.Fatalf("ios case-insensitive button: %+v", got)
	}
	if got := findElements(testflightList, []matcher{{Text: "example", Mode: "prefix"}}); len(got) == 0 {
		t.Fatal("prefix match")
	}
	if got := findElements("<not xml", installButtons); len(got) != 0 {
		t.Fatal("garbage source")
	}
}

func TestPlayStoreInstallFlowOverADB(t *testing.T) {
	var mu sync.Mutex
	var commands []string
	d := newDrivers(runnerConfig{})
	d.command = func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		joined := strings.Join(args, " ")
		commands = append(commands, joined)
		switch {
		case strings.Contains(joined, "screencap"):
			return testPNG(), nil
		case strings.Contains(joined, "'cat'"):
			return []byte(playListing), nil
		}
		return []byte("ok"), nil
	}
	s := Session{ID: "ses_1", Device: Device{Platform: "android", Driver: "adb", UDID: "SER"}}
	v, err := d.App(context.Background(), s, "install", map[string]any{"source": "store", "app_id": "com.example.game"})
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["result"] != "installing" {
		t.Fatalf("result: %+v", v)
	}
	all := strings.Join(commands, "\n")
	for _, want := range []string{"'market://details?id=com.example.game' '-p' 'com.android.vending'", "'input' 'tap' '800' '950'"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
}

func TestStoreInstallValidation(t *testing.T) {
	d := newDrivers(runnerConfig{})
	d.command = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("unused") }
	ios := Session{Device: Device{Platform: "ios", Driver: "appium"}}
	cases := []map[string]any{
		{"source": "store"},                    // App Store needs store_id
		{"source": "beta"},                     // TestFlight needs app_name or store_id
		{"source": "store", "store_id": "abc"}, // numeric only
		{"source": "warez", "app_id": "a.b"},   // unknown source
		{"source": "beta", "flow": []any{}},    // empty override
		{"source": "beta", "app_name": "x", "flow": []any{map[string]any{"do": "exec"}}},
	}
	for _, args := range cases {
		if _, err := d.App(context.Background(), ios, "install", args); errorCode(err) != "invalid_argument" {
			t.Errorf("%v: %v", args, err)
		}
	}
	if _, err := d.App(context.Background(), Session{Device: Device{Platform: "android", Driver: "adb"}}, "install", map[string]any{"source": "store"}); errorCode(err) != "invalid_argument" {
		t.Errorf("android without package: %v", err)
	}
}

func TestFlowTemplatesEscapeURLs(t *testing.T) {
	steps := expandFlow([]flowStep{{Do: "open_url", URL: "itms-beta://beta.itunes.apple.com/v1/app/{{store_id}}", App: "com.apple.TestFlight"}, {Do: "tap", Match: []matcher{{Text: "{{app_name}}"}}}}, map[string]string{"store_id": "123/../x", "app_name": "My Game"})
	if steps[0].URL != "itms-beta://beta.itunes.apple.com/v1/app/123%2F..%2Fx" || steps[1].Match[0].Text != "My Game" {
		t.Fatalf("expanded: %+v", steps)
	}
}

func TestADBAppStatus(t *testing.T) {
	d := newDrivers(runnerConfig{})
	d.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "'com.missing'") {
			return nil, errors.New("adb failed: ")
		}
		if strings.Contains(joined, "get-state") {
			return []byte("device"), nil
		}
		return []byte("package:/data/app/base.apk"), nil
	}
	s := Session{Device: Device{Platform: "android", Driver: "adb", UDID: "SER"}}
	for app, want := range map[string]bool{"com.present": true, "com.missing": false} {
		v, err := d.App(context.Background(), s, "status", map[string]any{"app_id": app})
		if err != nil || v.(map[string]any)["installed"] != want {
			t.Errorf("%s: %v %v", app, v, err)
		}
	}
}
