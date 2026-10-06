package main

import "testing"

func TestRunSpecAcceptsOnlySDKProjectRoutingMetadata(t *testing.T) {
	args := map[string]any{"name": "preview", "image": "oven/bun:1-debian", "_project_id": "untrusted-routing-value", "ports": []map[string]any{{"container_port": 3000, "host_port": 0, "bind_addr": "127.0.0.1", "protocol": "tcp"}}}
	spec, err := parseRunSpec(args)
	if err != nil || spec.Name != "preview" || len(spec.Ports) != 1 {
		t.Fatalf("SDK envelope rejected: %+v %v", spec, err)
	}
	if args["_project_id"] != "untrusted-routing-value" {
		t.Fatal("mutated caller arguments")
	}
	for _, key := range []string{"privileged", "_privileged", "unknown_runtime_flag"} {
		args[key] = true
		if _, err := parseRunSpec(args); err == nil {
			t.Fatalf("unknown field %q accepted", key)
		}
		delete(args, key)
	}
}
