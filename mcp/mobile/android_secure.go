package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// runPrivateCommand never surfaces subprocess output on errors. Input uses a
// pipe, not argv, a shell interpolation, a clipboard, or a file.
func runPrivateCommand(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(input)
	out := limitedBuffer{limit: 1024}
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return nil, errors.New("secure input failed; inspect privately before retrying")
	}
	return out.Bytes(), nil
}

func (d *drivers) SourceOnly(ctx context.Context, s Session) (Observation, error) {
	if s.Device.Driver != "adb" {
		return Observation{}, problem("unsupported", "private source requires Android ADB")
	}
	source, err := d.fastAndroidSource(ctx, s.Device.UDID)
	// No screenshot or legacy collector fallback on the private path.
	if err != nil {
		return Observation{}, problem("observation_unavailable", "private Android source unavailable")
	}
	return Observation{Source: source}, nil
}

func (d *drivers) androidSecureText(ctx context.Context, s Session, args map[string]any) (any, error) {
	fail := func() (any, error) {
		return nil, problem("secure_input_unconfirmed", "secure input not confirmed; inspect privately before retrying")
	}
	if s.Device.Driver != "adb" || s.Device.Platform != "android" || d.commandInput == nil {
		return fail()
	}
	password := str(args, "password")
	pkg := str(args, "expected_package")
	bounds := str(args, "expected_bounds")
	markers, ok := args["native_required"].([]any)
	if !ok {
		if v, yes := args["native_required"].([]string); yes {
			for _, s := range v {
				markers = append(markers, s)
			}
		}
	}
	values, ok := args["native_values"].(map[string]any)
	if !ok {
		if v, yes := args["native_values"].(map[string]string); yes {
			values = map[string]any{}
			for k, s := range v {
				values[k] = s
			}
		}
	}
	if len(password) == 0 || len(password) > 4096 || strings.ContainsAny(password, "\r\n\x00") || !appIDPattern.MatchString(pkg) || len(bounds) > 100 || bounds == "" || len(markers) < 1 || len(markers) > 12 || len(values) < 1 || len(values) > 12 || len(str(args, "expected_resource_id")) > 500 {
		return fail()
	}
	for _, v := range markers {
		s, yes := v.(string)
		if !yes || s == "" || len(s) > 500 || strings.Contains(s, " > ") {
			return fail()
		}
	}
	for k, v := range values {
		s, yes := v.(string)
		if !yes || len(k) == 0 || len(k) > 500 || len(s) > 4096 || strings.Contains(k, " > ") {
			return fail()
		}
	}
	// Only fixed guard fields and the secret are sent; no uncontrolled output keys.
	input, _ := json.Marshal(map[string]any{"password": password, "expected_package": pkg, "expected_bounds": bounds, "expected_resource_id": str(args, "expected_resource_id"), "native_required": markers, "native_values": values})
	if len(input) > 16384 {
		return fail()
	}
	f, err := os.CreateTemp("", "apteva-secure-helper-*.jar")
	if err != nil {
		return fail()
	}
	defer os.Remove(f.Name())
	_, err = f.Write(androidSourceJar)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return fail()
	}
	digest := sha256.Sum256(androidSourceJar)
	remote := fmt.Sprintf("/data/local/tmp/apteva-ui-source-%x.jar", digest[:8])
	if _, err = d.adb(ctx, s.Device.UDID, "push", f.Name(), remote); err != nil {
		return fail()
	}
	output, err := d.commandInput(ctx, input, "adb", "-s", s.Device.UDID, "shell", "-T", "CLASSPATH="+shellQuote(remote)+" app_process /system/bin AptevaSecureInput")
	if err != nil {
		return fail()
	}
	var receipt map[string]any
	if json.Unmarshal(output, &receipt) != nil || len(receipt) != 2 || receipt["secure_input"] != true || receipt["verified"] != true {
		return fail()
	}
	return map[string]any{"secure_input": true, "verified": true}, nil
}
