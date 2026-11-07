package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Explicit, bounded directed probes between registered hosts. Run the same
// target from a third instance for comparison; no provider is hard-coded.
// TCP counters are host-wide evidence, not attribution to this probe/call.
func networkEvidenceCommand(ip string, port, samples int) (string, error) {
	if net.ParseIP(ip) == nil || port < 1 || port > 65535 || samples < 1 || samples > 5 {
		return "", fmt.Errorf("valid IP, port and 1–5 samples required")
	}
	return fmt.Sprintf(`python3 - %s %d %d <<'APTEVA_NETWORK_EVIDENCE'
import datetime,json,socket,subprocess,sys,time
host,port,count=sys.argv[1],int(sys.argv[2]),int(sys.argv[3])
def now(): return datetime.datetime.now(datetime.timezone.utc).isoformat()
def command(args):
 try:
  r=subprocess.run(args,capture_output=True,text=True,timeout=2)
  return {"available":r.returncode==0,"output":r.stdout[:8192]}
 except (OSError,subprocess.TimeoutExpired): return {"available":False}
def counters():
 try:
  lines=open('/proc/net/snmp').read().splitlines()
  for i in range(len(lines)-1):
   if lines[i].startswith('Tcp:'):
    d=dict(zip(lines[i].split()[1:],lines[i+1].split()[1:]))
    return {k:int(d[k]) for k in ('ActiveOpens','AttemptFails','EstabResets','RetransSegs','InErrs','OutRsts') if k in d}
 except (OSError,ValueError): pass
 return None
out={"started_at":now(),"target_ip":host,"target_port":port,"tcp_counters_before":counters(),"samples":[]}
for i in range(count):
 t=time.monotonic(); row={"at":now()}
 try:
  with socket.create_connection((host,port),timeout=2): pass
  row['connected']=True
 except OSError as e: row.update(connected=False,error_type=type(e).__name__,errno=e.errno)
 row['duration_ms']=round((time.monotonic()-t)*1000,3); out['samples'].append(row)
 if i+1<count: time.sleep(.1)
out['tcp_counters_after']=counters()
out['route']=command(['ip','route','get',host])
out['completed_at']=now()
print(json.dumps(out))
APTEVA_NETWORK_EVIDENCE`, ip, port, samples), nil
}
func (a *App) toolNetworkEvidence(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	source, err := dbGetInstance(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	target, err := dbGetInstance(ctx.AppDB(), int64Arg(args, "target_id"))
	if err != nil {
		return nil, err
	}
	ip := target.PublicIPv4
	if ip == "" {
		ip = target.PublicIPv6
	}
	cmd, err := networkEvidenceCommand(ip, intArg(args, "port", 443), intArg(args, "samples", 3))
	if err != nil {
		return nil, err
	}
	// Use the independent administrative connection. Never the ingress pool.
	var output string
	var code int
	if source.IsLocal() {
		output, code, err = runLocal(cmd, 20*time.Second)
	} else {
		copy := *source
		work, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-work.Done():
			}
		}()
		copy.workContext = work
		output, code, err = runSSH(&copy, cmd, 20*time.Second)
	}
	if err != nil || code != 0 {
		return nil, fmt.Errorf("network evidence unavailable (requires Python 3 on source host)")
	}
	var evidence map[string]any
	if err = json.Unmarshal([]byte(output), &evidence); err != nil {
		return nil, fmt.Errorf("invalid network evidence response")
	}
	evidence["source_instance_id"] = source.ID
	evidence["target_instance_id"] = target.ID
	evidence["source_provider"] = source.Provider
	evidence["target_provider"] = target.Provider
	evidence["counter_scope"] = "source_host_cumulative"
	evidence["observed_at"] = time.Now().UTC()
	return evidence, nil
}
