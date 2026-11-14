package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestAndroidSecureInputPrivatePipeAndReceipt(t *testing.T) {
	const password = "fake-password-💬-%-$-test"
	for _, mode := range []string{"valid", "missing", "rejected", "unverified", "extra_output", "driver_error", "invalid_guard", "newline", "wrong_driver"} {
		t.Run(mode, func(t *testing.T) {
			d := newDrivers(runnerConfig{})
			calls := 0
			d.command = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if strings.Contains(strings.Join(args, " "), password) {
					t.Fatal("password in command")
				}
				if len(args) < 3 || args[2] != "push" {
					t.Fatal("unexpected command")
				}
				b, err := os.ReadFile(args[3])
				if err != nil || strings.Contains(string(b), password) {
					t.Fatal("private input written to helper file")
				}
				return nil, nil
			}
			d.commandInput = func(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
				calls++
				if strings.Contains(strings.Join(args, " "), password) {
					t.Fatal("password in argv")
				}
				var in map[string]any
				if json.Unmarshal(input, &in) != nil || in["password"] != password {
					t.Fatal("missing private input")
				}
				switch mode {
				case "missing":
					return []byte(`{}`), nil
				case "rejected":
					return []byte(`{"secure_input":false,"verified":true}`), nil
				case "unverified":
					return []byte(`{"secure_input":true,"verified":false}`), nil
				case "extra_output":
					return []byte(`{"secure_input":true,"verified":true,"text":"` + password + `"}`), nil
				case "driver_error":
					return []byte(password), errors.New(password)
				}
				return []byte(`{"secure_input":true,"verified":true}`), nil
			}
			args := map[string]any{"password": password, "expected_package": "com.example", "expected_bounds": "[0,20][100,60]", "expected_resource_id": "password", "native_required": []string{"Log in"}, "native_values": map[string]string{"username": "test.user"}}
			s := Session{Device: Device{Platform: "android", Driver: "adb", UDID: "test"}}
			if mode == "invalid_guard" {
				delete(args, "native_values")
			}
			if mode == "newline" {
				args["password"] = password + "\n"
			}
			if mode == "wrong_driver" {
				s.Device.Driver = "appium"
			}
			out, err := d.androidSecureText(context.Background(), s, args)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("unexpected outcome %v", err)
			}
			b, _ := json.Marshal(out)
			if strings.Contains(string(b), password) || (err != nil && strings.Contains(err.Error(), password)) {
				t.Fatal("secret escaped response")
			}
			if (mode == "invalid_guard" || mode == "newline" || mode == "wrong_driver") && calls != 0 {
				t.Fatal("invalid input dispatched")
			}
		})
	}
}
func TestAndroidPrivateSourceNeverCapturesImage(t *testing.T) {
	d := newDrivers(runnerConfig{})
	commands := []string{}
	d.command = func(_ context.Context, name string, args ...string) ([]byte, error) {
		commands = append(commands, strings.Join(args, " "))
		if args[2] == "push" {
			return nil, nil
		}
		return []byte(`<hierarchy><node package="com.example"/></hierarchy>`), nil
	}
	obs, err := d.SourceOnly(context.Background(), Session{Device: Device{Driver: "adb", UDID: "test"}})
	if err != nil || obs.Image != "" || obs.Source == "" {
		t.Fatalf("private observation failed: %v", err)
	}
	for _, command := range commands {
		if strings.Contains(command, "screencap") {
			t.Fatal("captured private screen")
		}
	}
}

func TestSecureInputRejectsActiveDeviceRecording(t *testing.T) {
	for _, status := range []string{"recording", "processing"} {
		t.Run(status, func(t *testing.T) {
			e, b := fixture(t)
			s := openFixture(t, e, "p")
			if err := e.insertRecording(Recording{ID: "rec-private", ProjectID: "p", SessionID: s.ID, DeviceID: s.DeviceID, Status: status}); err != nil {
				t.Fatal(err)
			}
			_, err := e.Execute(context.Background(), "p", "mobile_action", map[string]any{"session_id": s.ID, "action": "secure_text", "password": "fixture-only"})
			b.mu.Lock()
			calls := b.actions
			b.mu.Unlock()
			if err == nil || calls != 0 {
				t.Fatalf("recorded secure input was dispatched: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestPrivateObservationRejectsUnsupportedOrMalformedRequests(t *testing.T) {
	for _, args := range []map[string]any{{"source": true, "image": false}, {"source": false, "image": false}, {"source": true, "image": "false"}} {
		e, _ := fixture(t)
		s := openFixture(t, e, "p")
		args["session_id"] = s.ID
		// This backend only supports screenshot observation. Private calls must fail,
		// rather than silently capturing an image as a fallback.
		if _, err := e.Execute(context.Background(), "p", "mobile_observe", args); err == nil {
			t.Fatal("unsafe private observation accepted")
		}
	}
}
