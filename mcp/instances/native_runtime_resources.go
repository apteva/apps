package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"reflect"
	"strconv"
)

func nativeResourceCapabilities() *sdk.ResourceCapabilities {
	return &sdk.ResourceCapabilities{Contract: sdk.ResourcePolicyVersion, ProcessControls: []string{"cpu_weight", "cpu_quota_percent", "memory_high_bytes", "memory_max_bytes", "io_weight", "nice"}, ProcessApply: "next_deployment", WorkloadApply: "cooperative", NetworkApply: "new_sockets", Limitations: []string{"systemd/cgroup support is target-dependent", "ancestor quotas and host contention remain applicable", "DSCP enforcement beyond the host is not guaranteed"}}
}

func nativeEffectiveResourcePolicy(p *sdk.ResourcePolicy) *sdk.ResourcePolicy {
	if p == nil || p.Process.Empty() && p.TotalConcurrency == 0 && len(p.Workloads) == 0 && len(p.Network) == 0 {
		return nil
	}
	return p
}
func validateNativeResourceEnvironment(d nativeRecord, env map[string]string) error {
	raw := env["APTEVA_RESOURCE_POLICY"]
	if raw == "" {
		if d.Options.Resources != nil {
			return fmt.Errorf("resource policy environment missing")
		}
		return nil
	}
	var envelope sdk.ResourceEnvelope
	if e := json.Unmarshal([]byte(raw), &envelope); e != nil {
		return fmt.Errorf("invalid resource policy environment")
	}
	if envelope.InstallID != strconv.FormatInt(d.InstallID, 10) || envelope.DeploymentID != d.ID || envelope.Generation != strconv.FormatInt(d.Generation, 10) {
		return fmt.Errorf("resource policy identity mismatch")
	}
	if d.Options.Resources != nil && !reflect.DeepEqual(envelope.Policy, *d.Options.Resources) {
		return fmt.Errorf("resource policy differs from prepared policy")
	}
	return envelope.Policy.Validate()
}

// This read-only inspection shares the existing bounded SSH request. Kernel
// values are evidence; absence stays unavailable rather than echoing requests.
func nativeResourceInspectionScript() string {
	return `
 resource_json='{}'
 if test "$observed" = running; then
  case "$cg" in /*) ;; *) cg='' ;; esac
  case "$cg" in *..*) cg='' ;; esac
  if test -n "$cg" && test -d "/sys/fs/cgroup$cg"; then
   resource_json='{'; sep=''
   for key in cpu.weight cpu.max memory.high memory.max io.weight cpu.stat cpu.pressure memory.pressure io.pressure; do
    value=$(head -c 512 "/sys/fs/cgroup$cg/$key" 2>/dev/null | tr '\n' ';' || true)
    case "$value" in ''|*[!a-zA-Z0-9_.\ /\;=]*) value=unavailable ;; esac
    resource_json="$resource_json$sep\"$key\":\"$value\""; sep=','
   done
   resource_json="$resource_json}"
  fi
 fi
 `
}
