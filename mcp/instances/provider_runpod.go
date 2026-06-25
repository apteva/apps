package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const defaultRunPodImage = "runpod/pytorch:2.1.0-py3.10-cuda11.8.0-devel-ubuntu22.04"
const defaultRunPodGPU = "NVIDIA GeForce RTX 4090"

type InstanceResources struct {
	CPU          *CPUResource     `json:"cpu,omitempty"`
	MemoryGB     float64          `json:"memory_gb,omitempty"`
	DiskGB       int              `json:"disk_gb,omitempty"`
	Accelerators []AcceleratorDef `json:"accelerators,omitempty"`
}

type CPUResource struct {
	Cores float64 `json:"cores,omitempty"`
}

type AcceleratorDef struct {
	Kind     string  `json:"kind"`
	Vendor   string  `json:"vendor,omitempty"`
	Model    string  `json:"model,omitempty"`
	Count    int     `json:"count,omitempty"`
	MemoryGB float64 `json:"memory_gb,omitempty"`
}

type runPodPod struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	DesiredStatus     string         `json:"desiredStatus"`
	Status            string         `json:"status"`
	RuntimeStatus     string         `json:"runtimeStatus"`
	PublicIP          string         `json:"publicIp"`
	PortMappings      map[string]any `json:"portMappings"`
	ImageName         string         `json:"imageName"`
	TemplateID        string         `json:"templateId"`
	DataCenterID      string         `json:"dataCenterId"`
	GPUTypeID         string         `json:"gpuTypeId"`
	GPUTypeIDs        []string       `json:"gpuTypeIds"`
	GPUCount          int            `json:"gpuCount"`
	VCPUCount         float64        `json:"vcpuCount"`
	MemoryInGB        float64        `json:"memoryInGb"`
	ContainerDiskInGB int            `json:"containerDiskInGb"`
	VolumeInGB        int            `json:"volumeInGb"`
}

func runPodBound(ctx *sdk.AppCtx) (*sdk.BoundIntegration, error) {
	bound := ctx.IntegrationFor("provider")
	if bound == nil || bound.ConnectionID == 0 {
		return nil, errors.New("no VPS provider bound — bind a RunPod connection on the Instances install")
	}
	if bound.AppSlug != "" && bound.AppSlug != "runpod" {
		return nil, fmt.Errorf("runpod adapter requires provider=runpod; bound slug is %q", bound.AppSlug)
	}
	return bound, nil
}

func runPodListServerTypes(ctx *sdk.AppCtx) ([]ServerType, error) {
	if _, err := runPodBound(ctx); err != nil {
		return nil, err
	}
	return []ServerType{
		runPodGPUType("NVIDIA GeForce RTX 4090", 24),
		runPodGPUType("NVIDIA L40S", 48),
		runPodGPUType("NVIDIA L4", 24),
		runPodGPUType("NVIDIA A100 80GB PCIe", 80),
		runPodGPUType("NVIDIA H100 80GB HBM3", 80),
		{
			Name:         "cpu:8",
			Description:  "RunPod CPU Pod, 8 vCPU requested",
			Cores:        8,
			MemoryGB:     16,
			DiskGB:       50,
			CPUType:      "shared",
			Architecture: "x86",
		},
	}, nil
}

func runPodGPUType(name string, vramGB int) ServerType {
	return ServerType{
		Name:         name,
		Description:  fmt.Sprintf("RunPod GPU Pod, 1x %s", name),
		Cores:        0,
		MemoryGB:     float64(vramGB),
		DiskGB:       50,
		CPUType:      "gpu",
		Architecture: "x86",
	}
}

func runPodListLocations(ctx *sdk.AppCtx) ([]Location, error) {
	if _, err := runPodBound(ctx); err != nil {
		return nil, err
	}
	return []Location{
		{Name: "EU-RO-1", Country: "RO", Description: "RunPod Romania"},
		{Name: "EU-NL-1", Country: "NL", Description: "RunPod Netherlands"},
		{Name: "EU-SE-1", Country: "SE", Description: "RunPod Sweden"},
		{Name: "US-TX-1", Country: "US", Description: "RunPod Texas"},
		{Name: "US-TX-3", Country: "US", Description: "RunPod Texas"},
		{Name: "US-CA-2", Country: "US", Description: "RunPod California"},
		{Name: "US-GA-2", Country: "US", Description: "RunPod Georgia"},
		{Name: "CA-MTL-1", Country: "CA", Description: "RunPod Montreal"},
		{Name: "OC-AU-1", Country: "AU", Description: "RunPod Australia"},
	}, nil
}

func runPodListImages(ctx *sdk.AppCtx) ([]Image, error) {
	if _, err := runPodBound(ctx); err != nil {
		return nil, err
	}
	return []Image{
		{
			Name:         defaultRunPodImage,
			Description:  "RunPod PyTorch CUDA Ubuntu image with SSH bootstrap support",
			OSFlavor:     "ubuntu",
			OSVersion:    "22.04",
			Architecture: "x86",
		},
	}, nil
}

func runPodProvision(ctx *sdk.AppCtx, in CreateInstanceInput) (*Instance, error) {
	bound, err := runPodBound(ctx)
	if err != nil {
		return nil, err
	}
	privKey, pubKey, err := generateSSHKeypair()
	if err != nil {
		return nil, fmt.Errorf("generate ssh keypair: %w", err)
	}
	if in.Image == "" {
		in.Image = defaultRunPodImage
	}
	if in.Size == "" {
		in.Size = defaultRunPodGPU
	}
	if in.Region == "" {
		in.Region = "EU-RO-1"
	}
	computeType, gpuType, gpuCount, vcpuCount := parseRunPodSize(in.Size)
	resourcesJSON := runPodResourcesJSON(computeType, gpuType, gpuCount, vcpuCount, 50, 20)

	in.Provider = "runpod"
	in.Status = "provisioning"
	in.SSHUser = "root"
	in.SSHPort = 22
	in.SSHPrivateKey = privKey
	in.SSHPublicKey = pubKey
	in.ResourcesJSON = resourcesJSON
	in.PortsJSON = "{}"

	inst, err := dbCreateInstance(ctx.AppDB(), in)
	if err != nil {
		return nil, err
	}
	emitInstanceCreated(ctx, inst)
	emitInstanceStatus(ctx, inst)

	args := map[string]any{
		"name":               in.Name,
		"computeType":        computeType,
		"cloudType":          "SECURE",
		"imageName":          in.Image,
		"dataCenterIds":      []string{in.Region},
		"dataCenterPriority": "availability",
		"supportPublicIp":    true,
		"ports":              []string{"22/tcp"},
		"env": map[string]any{
			"APTEVA_SSH_PUBLIC_KEY": pubKey,
		},
		"dockerEntrypoint":  []string{"/bin/bash", "-lc"},
		"dockerStartCmd":    []string{runPodSSHBootstrapScript()},
		"containerDiskInGb": 50,
		"volumeInGb":        20,
		"volumeMountPath":   "/workspace",
	}
	if computeType == "GPU" {
		args["gpuTypeIds"] = []string{gpuType}
		args["gpuTypePriority"] = "availability"
		args["gpuCount"] = gpuCount
		args["minVCPUPerGPU"] = 2
		args["minRAMPerGPU"] = 8
	} else {
		args["vcpuCount"] = vcpuCount
	}

	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "create_pod", args)
	if err != nil {
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
			"status":        "error",
			"error_message": fmt.Sprintf("runpod.create_pod: %v", err),
		})
		return nil, fmt.Errorf("runpod.create_pod: %w", err)
	}
	if res == nil || !res.Success {
		msg := upstreamErrorString(res)
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
			"status":        "error",
			"error_message": msg,
		})
		return nil, fmt.Errorf("runpod.create_pod returned status=%d: %s", upstreamStatus(res), msg)
	}

	pod := parseRunPodPodResponse(res.Data)
	if pod.ID == "" {
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
			"status":        "error",
			"error_message": "runpod.create_pod response missing pod id; catalog shape may be out of sync with upstream API",
		})
		return nil, errors.New("runpod.create_pod response missing pod id")
	}
	updateRunPodInstanceFromPod(ctx, inst.ID, pod)
	kickRunPodReadinessProbe(ctx, inst.ID)
	return dbGetInstance(ctx.AppDB(), inst.ID)
}

func runPodDestroy(ctx *sdk.AppCtx, inst *Instance) error {
	bound, err := runPodBound(ctx)
	if err != nil {
		return err
	}
	if inst.ProviderID == "" {
		return nil
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "delete_pod", map[string]any{
		"podId": inst.ProviderID,
	})
	if err != nil {
		return fmt.Errorf("runpod.delete_pod: %w", err)
	}
	if res == nil || !res.Success {
		if upstreamStatus(res) == 404 {
			return nil
		}
		return fmt.Errorf("runpod.delete_pod returned: %s", upstreamErrorString(res))
	}
	globalSSHPool.evict(inst.ID)
	return nil
}

func kickRunPodReadinessProbe(ctx *sdk.AppCtx, id int64) {
	go func() {
		fresh, err := dbGetInstance(ctx.AppDB(), id)
		if err != nil {
			return
		}
		if fresh.PublicIPv4 == "" || fresh.SSHPort <= 0 || fresh.SSHPort == 22 {
			fresh, err = waitRunPodNetwork(ctx, fresh, 10*time.Minute)
			if err != nil {
				_, _ = updateInstanceAndEmit(ctx, id, map[string]any{
					"status":        "error",
					"error_message": fmt.Sprintf("runpod network: %v", err),
				})
				return
			}
		}
		if err := probeSSHReady(fresh, 10*time.Minute); err != nil {
			_, _ = updateInstanceAndEmit(ctx, id, map[string]any{
				"status":        "error",
				"error_message": fmt.Sprintf("ssh probe: %v", err),
			})
			return
		}
		_, _ = updateInstanceAndEmit(ctx, id, map[string]any{
			"status":   "ready",
			"ready_at": nowUTC(),
		})
	}()
}

func waitRunPodNetwork(ctx *sdk.AppCtx, inst *Instance, timeout time.Duration) (*Instance, error) {
	bound, err := runPodBound(ctx)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "get_pod", map[string]any{
			"podId": inst.ProviderID,
		})
		if err != nil {
			return nil, fmt.Errorf("get_pod: %w", err)
		}
		if res == nil || !res.Success {
			return nil, fmt.Errorf("get_pod: %s", upstreamErrorString(res))
		}
		pod := parseRunPodPodResponse(res.Data)
		updateRunPodInstanceFromPod(ctx, inst.ID, pod)
		if pod.PublicIP != "" && runPodSSHPort(pod.PortMappings) > 0 {
			return dbGetInstance(ctx.AppDB(), inst.ID)
		}
		if strings.EqualFold(pod.DesiredStatus, "TERMINATED") || strings.EqualFold(pod.Status, "TERMINATED") {
			return nil, errors.New("pod terminated before SSH endpoint became available")
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for public IP and 22/tcp mapping")
		}
		time.Sleep(5 * time.Second)
	}
}

func reconcileRunPodProvisioning(ctx *sdk.AppCtx) {
	rows, err := dbListInstances(ctx.AppDB(), "runpod", "provisioning")
	if err != nil {
		ctx.Logger().Warn("instances: reconcile runpod list failed", "err", err)
		return
	}
	for _, inst := range rows {
		if inst.ProviderID != "" {
			kickRunPodReadinessProbe(ctx, inst.ID)
			continue
		}
		_, _ = updateInstanceAndEmit(ctx, inst.ID, map[string]any{
			"status":        "error",
			"error_message": "provisioning interrupted before RunPod pod id was recorded — Instances will not infer a pod by name; check the RunPod dashboard for an orphan pod named " + inst.Name,
		})
	}
}

func updateRunPodInstanceFromPod(ctx *sdk.AppCtx, id int64, pod runPodPod) {
	fields := map[string]any{}
	if pod.ID != "" {
		fields["provider_id"] = pod.ID
	}
	if pod.PublicIP != "" {
		fields["public_ipv4"] = pod.PublicIP
		fields["ssh_host"] = pod.PublicIP
	}
	if port := runPodSSHPort(pod.PortMappings); port > 0 {
		fields["ssh_port"] = port
	}
	if pod.DataCenterID != "" {
		fields["region"] = pod.DataCenterID
	}
	if pod.ImageName != "" {
		fields["image"] = pod.ImageName
	}
	if ports := marshalJSONString(pod.PortMappings, "{}"); ports != "{}" {
		fields["ports_json"] = ports
	}
	if res := runPodResourcesFromPod(pod); res != "" && res != "{}" {
		fields["resources_json"] = res
	}
	_ = dbUpdateInstance(ctx.AppDB(), id, fields)
}

func parseRunPodSize(size string) (computeType, gpuType string, gpuCount int, vcpuCount int) {
	size = strings.TrimSpace(size)
	if size == "" {
		size = defaultRunPodGPU
	}
	if strings.HasPrefix(strings.ToLower(size), "cpu") {
		computeType = "CPU"
		gpuCount = 0
		vcpuCount = 8
		re := regexp.MustCompile(`([0-9]+)`)
		if m := re.FindString(size); m != "" {
			if n, err := strconv.Atoi(m); err == nil && n > 0 {
				vcpuCount = n
			}
		}
		return computeType, "", gpuCount, vcpuCount
	}
	computeType = "GPU"
	gpuCount = 1
	re := regexp.MustCompile(`(?i)\s+x([0-9]+)$`)
	if m := re.FindStringSubmatch(size); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			gpuCount = n
			size = strings.TrimSpace(size[:len(size)-len(m[0])])
		}
	}
	return computeType, size, gpuCount, 0
}

func runPodSSHBootstrapScript() string {
	return strings.Join([]string{
		"set -e",
		"mkdir -p /root/.ssh /run/sshd",
		"printf '%s\\n' \"$APTEVA_SSH_PUBLIC_KEY\" > /root/.ssh/authorized_keys",
		"chmod 700 /root/.ssh",
		"chmod 600 /root/.ssh/authorized_keys",
		"if ! command -v sshd >/dev/null 2>&1; then",
		"  if command -v apt-get >/dev/null 2>&1; then",
		"    apt-get update",
		"    DEBIAN_FRONTEND=noninteractive apt-get install -y openssh-server",
		"  elif command -v apk >/dev/null 2>&1; then",
		"    apk add --no-cache openssh",
		"  else",
		"    echo 'openssh-server not installed and no supported package manager found' >&2",
		"    exit 1",
		"  fi",
		"fi",
		"exec /usr/sbin/sshd -D -e",
	}, "\n")
}

func parseRunPodPodResponse(data json.RawMessage) runPodPod {
	for _, key := range []string{"pod", "data"} {
		var wrapped map[string]json.RawMessage
		if err := json.Unmarshal(data, &wrapped); err == nil {
			if raw, ok := wrapped[key]; ok {
				var pod runPodPod
				if json.Unmarshal(raw, &pod) == nil {
					return pod
				}
			}
		}
	}
	var pod runPodPod
	_ = json.Unmarshal(data, &pod)
	return pod
}

func runPodSSHPort(m map[string]any) int {
	for _, key := range []string{"22", "22/tcp"} {
		if n := intFromAny(m[key]); n > 0 {
			return n
		}
	}
	return 0
}

func runPodResourcesFromPod(pod runPodPod) string {
	res := InstanceResources{
		CPU:      &CPUResource{Cores: pod.VCPUCount},
		MemoryGB: pod.MemoryInGB,
		DiskGB:   pod.ContainerDiskInGB + pod.VolumeInGB,
	}
	gpuType := pod.GPUTypeID
	if gpuType == "" && len(pod.GPUTypeIDs) > 0 {
		gpuType = pod.GPUTypeIDs[0]
	}
	if gpuType != "" {
		count := pod.GPUCount
		if count <= 0 {
			count = 1
		}
		res.Accelerators = []AcceleratorDef{runPodAccelerator(gpuType, count)}
	}
	return marshalJSONString(res, "{}")
}

func runPodResourcesJSON(computeType, gpuType string, gpuCount, vcpuCount, containerDisk, volumeDisk int) string {
	res := InstanceResources{
		DiskGB: containerDisk + volumeDisk,
	}
	if computeType == "CPU" {
		res.CPU = &CPUResource{Cores: float64(vcpuCount)}
	} else if gpuType != "" {
		res.Accelerators = []AcceleratorDef{runPodAccelerator(gpuType, gpuCount)}
	}
	return marshalJSONString(res, "{}")
}

func runPodAccelerator(gpuType string, count int) AcceleratorDef {
	if count <= 0 {
		count = 1
	}
	return AcceleratorDef{
		Kind:     "gpu",
		Vendor:   "nvidia",
		Model:    strings.TrimPrefix(gpuType, "NVIDIA "),
		Count:    count,
		MemoryGB: runPodVRAMGB(gpuType),
	}
}

func runPodVRAMGB(gpuType string) float64 {
	lower := strings.ToLower(gpuType)
	switch {
	case strings.Contains(lower, "h100"), strings.Contains(lower, "a100 80"):
		return 80
	case strings.Contains(lower, "l40s"):
		return 48
	case strings.Contains(lower, "4090"), strings.Contains(lower, "l4"):
		return 24
	default:
		return 0
	}
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		return 0
	}
}

func marshalJSONString(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	b, err := json.Marshal(v)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return fallback
	}
	return string(b)
}
