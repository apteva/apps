package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var ovhDeliveryPoll = 30 * time.Second
var ovhDeliveryDeadline = 24 * time.Hour
var errOVHPending = errors.New("OVH operation pending")

func kickOVHContract(ctx *sdk.AppCtx, id int64) {
	probe := probeSSHReadyFn
	startInstanceWorker(ctx, id, func(work context.Context) {
		deadline := time.Now().Add(ovhDeliveryDeadline)
		for {
			inst, e := dbGetInstance(ctx.AppDB(), id)
			if e != nil || (inst.Status != "provisioning" && inst.Status != "destroying") {
				return
			}
			inst.workContext = work
			r, e := ovhOrderForInstance(ctx, inst)
			if e != nil {
				failInstanceStage(ctx, id, "Order", e)
				return
			}
			if inst.Status == "destroying" && r.State.ServiceName != "" {
				_ = resumeInstanceDestroy(ctx, inst)
				return
			}
			if e = ovhContractStep(ctx, inst, r, probe); e == nil {
				return
			} else if !errors.Is(e, errOVHPending) {
				_, _ = ctx.AppDB().Exec(`UPDATE provider_orders SET state_json=json_set(state_json,'$.error',?),updated_at=? WHERE id=? AND connection_id=?`, e.Error(), nowUTC(), r.ID, r.ConnectionID)
				_ = dbUpdateInstance(ctx.AppDB(), id, map[string]any{"error_message": e.Error(), "lifecycle_stage": r.State.Phase})
				return
			}
			if time.Now().After(deadline) {
				_ = dbUpdateInstance(ctx.AppDB(), id, map[string]any{"error_message": "OVH delivery/install is still pending; retry wait-ready to resume without ordering again"})
				return
			}
			if sleepContext(work, ovhDeliveryPoll) != nil {
				return
			}
		}
	})
}
func ovhContractStep(ctx *sdk.AppCtx, inst *Instance, r *ovhOrderRecord, probe func(*Instance, time.Duration) error) error {
	unlock, lockErr := ovhLockOrder(ctx, r.ID)
	if lockErr != nil {
		return errOVHPending
	}
	defer unlock()
	fresh, err := dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		return err
	}
	fresh.workContext = inst.workContext
	inst = fresh
	if inst.Status != "provisioning" && inst.Status != "destroying" {
		return errOVHPending
	}
	recovered, err := ovhReadOrder(ctx, r.ID)
	if err != nil {
		return err
	}
	*r = *recovered

	if r.State.OrderID == 0 {
		// A cart read may expose a committed order after a lost acknowledgement.
		// No background recovery path calls checkout again.
		result, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", "cart_quote", map[string]any{"cartId": r.State.CartID})
		if e != nil {
			return e
		}
		id := anyToInt64JSON(result, "orderId")
		if id <= 0 {
			return errOVHPending
		}
		if ovhQuoteDigest(result) != r.State.QuoteDigest {
			return errors.New("OVH recovered cart contents do not match the reviewed quote")
		}
		r.State.OrderID = id
		r.State.Phase = "delivery"
		r.State.Error = ""
		if e = ovhSaveOrder(ctx, r); e != nil {
			return e
		}
		if e = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"create_pending": false}); e != nil {
			return e
		}
	}
	product, plan := ovhSizeParts(inst.Size)
	if r.State.ServiceName == "" {
		statusJSON, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", "order_status", map[string]any{"orderId": r.State.OrderID})
		if e != nil {
			return e
		}
		var status string
		if e = json.Unmarshal(statusJSON, &status); e != nil {
			return e
		}
		if r.State.DeliveryStatus != status {
			r.State.DeliveryStatus = status
			if e = ovhSaveOrder(ctx, r); e != nil {
				return e
			}
		}
		if status == "cancelled" || status == "cancelling" {
			return errors.New("OVH order is cancelled/cancelling; inventory retained until delivery and billing are reconciled")
		}
		if status != "delivered" {
			return errOVHPending
		}
		service, e := ovhDeliveredService(ctx, r, product, plan)
		if e != nil {
			return e
		}
		if service == "" {
			return errOVHPending
		}
		r.State.ServiceName = service
		r.State.Phase = "install"
		if e = ovhSaveOrder(ctx, r); e != nil {
			return e
		}
		if e = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"provider_id": service, "lifecycle_stage": "Install", "create_pending": false}); e != nil {
			return e
		}
		inst.ProviderID = service
	}
	if inst.Status == "destroying" {
		if e := dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"create_pending": false}); e != nil {
			return e
		}
		return errOVHPending
	}
	if !r.State.InstallStarted {
		if e := nativeHostRetained(ctx.AppDB(), inst.ID); e != nil {
			return errors.New("OVH host retains deployment data; installation refused")
		}
		if _, e := ovhContractNetwork(ctx, inst, r); e != nil {
			return errOVHPending
		}
		tool, args, e := ovhInstallRequest(ctx, inst, r)
		if e != nil {
			return e
		}
		current, e := dbGetInstance(ctx.AppDB(), inst.ID)
		if e != nil {
			return e
		}
		if current.Status != "provisioning" {
			return errOVHPending
		}
		// Persist before a destructive operation. An interrupted response must never
		// trigger a second reinstall and erase data written after the first one.
		r.State.InstallStarted = true
		r.State.Phase = "install_unknown"
		if e = ovhSaveOrder(ctx, r); e != nil {
			return e
		}
		data, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", tool, args)
		if e != nil {
			r.State.Error = "OVH installation outcome is unknown; inspecting with the generated SSH key. No reinstall will be repeated."
			_ = ovhSaveOrder(ctx, r)
			return errOVHPending
		}
		r.State.TaskID = anyToInt64JSON(data, "id")
		if product != "vps" {
			r.State.TaskID = anyToInt64JSON(data, "taskId")
		}
		r.State.Phase = "install"
		if e = ovhSaveOrder(ctx, r); e != nil {
			return e
		}
	}
	if r.State.TaskID > 0 {
		tool, args := "vps_task_get", map[string]any{"serviceName": r.State.ServiceName, "id": r.State.TaskID}
		if product != "vps" {
			tool = "dedicated_task_get"
			args = map[string]any{"serviceName": r.State.ServiceName, "taskId": r.State.TaskID}
		}
		data, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", tool, args)
		if e != nil {
			return e
		}
		state := firstNonEmpty(jsonStringAt(data, "state"), jsonStringAt(data, "status"))
		if state != "done" {
			switch state {
			case "error", "cancelled", "customerError", "ovhError", "blocked":
				return fmt.Errorf("OVH installation task failed: %s", state)
			}
			return errOVHPending
		}
	}
	fresh, e := ovhContractNetwork(ctx, inst, r)
	if e != nil {
		return e
	}
	if fresh.PublicIPv4 == "" && fresh.PublicIPv6 == "" {
		return errOVHPending
	}
	fresh.workContext = inst.workContext
	if e = probe(fresh, 30*time.Second); e != nil {
		return errOVHPending
	}
	current, err := dbGetInstance(ctx.AppDB(), inst.ID)
	if err != nil {
		return err
	}
	if current.Status != "provisioning" {
		return errOVHPending
	}
	r.State.Phase = "ready"
	r.State.Error = ""
	if e = ovhSaveOrder(ctx, r); e != nil {
		return e
	}
	_, _, e = transitionInstanceAndEmit(ctx, inst.ID, []string{"provisioning"}, "ready", map[string]any{"ready_at": nowUTC(), "error_message": "", "primary_error": "", "lifecycle_stage": "Ready"})
	return e
}
func ovhDeliveredService(ctx *sdk.AppCtx, r *ovhOrderRecord, product, plan string) (string, error) {
	data, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", "order_details_list", map[string]any{"orderId": r.State.OrderID})
	if e != nil {
		return "", e
	}
	var ids []int64
	if e = json.Unmarshal(data, &ids); e != nil {
		return "", e
	}
	if len(ids) > 512 {
		return "", errors.New("OVH order contains too many details")
	}
	services := []string{}
	for _, id := range ids {
		args := map[string]any{"orderId": r.State.OrderID, "orderDetailId": id}
		ext, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", "order_detail_extension", args)
		if e != nil {
			return "", e
		}
		var root map[string]any
		if e = json.Unmarshal(ext, &root); e != nil {
			return "", e
		}
		if nestedMapString(root, "order", "type") != "plan" || nestedMapString(root, "order", "plan", "code") != plan || nestedMapString(root, "order", "action") != "installation" {
			continue
		}
		detail, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", "order_detail_get", args)
		if e != nil {
			return "", e
		}
		domain := jsonStringAt(detail, "domain")
		if domain != "" {
			// Exact service identity comes only from the paid order's own line item.
			if strings.ContainsAny(domain, "/\\ \t\n") {
				return "", errors.New("invalid OVH delivered service identity")
			}
			services = append(services, domain)
			r.State.MainDetailID = id
		}
	}
	if len(services) > 1 {
		return "", errors.New("OVH order has ambiguous main services; no installation attempted")
	}
	if len(services) == 0 {
		return "", nil
	}
	return services[0], nil
}
func ovhInstallRequest(ctx *sdk.AppCtx, inst *Instance, r *ovhOrderRecord) (string, map[string]any, error) {
	product, _ := ovhSizeParts(inst.Size)
	if product == "vps" {
		ids, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", "vps_images_list", map[string]any{"serviceName": r.State.ServiceName})
		if e != nil {
			return "", nil, e
		}
		var images []string
		if e = json.Unmarshal(ids, &images); e != nil {
			return "", nil, e
		}
		if len(images) > 512 {
			return "", nil, errors.New("OVH image list too large")
		}
		target := strings.TrimPrefix(inst.Image, "ovh-vps/")
		imageID := ""
		for _, id := range images {
			data, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", "vps_image_get", map[string]any{"serviceName": r.State.ServiceName, "id": id})
			if e != nil {
				return "", nil, e
			}
			if jsonStringAt(data, "name") == target {
				imageID = id
				break
			}
		}
		if imageID == "" {
			return "", nil, errors.New("selected Linux image is not compatible with the delivered VPS")
		}
		script := "#!/bin/sh\nset -eu\ninstall -d -m 700 /root/.ssh\nprintf '%s\\n' " + shellSingleQuote(strings.TrimSpace(r.State.PublicKey)) + " >> /root/.ssh/authorized_keys\nchmod 600 /root/.ssh/authorized_keys\nmkdir -p /etc/ssh/sshd_config.d\nprintf '%s\\n' 'PermitRootLogin prohibit-password' > /etc/ssh/sshd_config.d/00-apteva-key-access.conf\nsystemctl reload ssh 2>/dev/null || systemctl reload sshd\n"
		return "vps_rebuild", map[string]any{"serviceName": r.State.ServiceName, "imageId": imageID, "publicSshKey": strings.TrimSpace(r.State.PublicKey), "doNotSendPassword": true, "installRTM": false, "postInstallScript": script}, nil
	}
	data, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", "dedicated_compatible_images", map[string]any{"serviceName": r.State.ServiceName})
	if e != nil {
		return "", nil, e
	}
	var compatible map[string][]string
	if e = json.Unmarshal(data, &compatible); e != nil {
		return "", nil, e
	}
	image := strings.TrimPrefix(inst.Image, "ovh-dedicated/")
	if !containsString(compatible["ovh"], image) || !ovhLinuxName(image) {
		return "", nil, errors.New("selected Linux template is incompatible with the delivered dedicated server")
	}
	return "dedicated_reinstall", map[string]any{"serviceName": r.State.ServiceName, "operatingSystem": image, "customizations": map[string]any{"hostname": providerHostname(inst.Name), "sshKey": strings.TrimSpace(r.State.PublicKey), "configDriveUserData": base64.StdEncoding.EncodeToString([]byte(buildCloudInit(r.State.PublicKey)))}}, nil
}
func ovhContractNetwork(ctx *sdk.AppCtx, inst *Instance, r *ovhOrderRecord) (*Instance, error) {
	product, _ := ovhSizeParts(inst.Size)
	tool := ovhProductTool(product)
	if product == "eco" {
		tool = "dedicated"
	}
	server, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", tool+"_get", map[string]any{"serviceName": r.State.ServiceName})
	if e != nil {
		return nil, e
	}
	ipv4, ipv6 := "", ""
	if product != "vps" {
		ipv4 = usableProviderIP(jsonStringAt(server, "ip"), 4)
	}
	ips, e := executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", tool+"_ips", map[string]any{"serviceName": r.State.ServiceName})
	if e != nil {
		return nil, e
	}
	var addresses []string
	if e = json.Unmarshal(ips, &addresses); e != nil {
		return nil, e
	}
	for _, ip := range addresses {
		if a := usableProviderIP(ip, 4); a != "" {
			ipv4 = a
		}
		if a := usableProviderIP(ip, 6); a != "" {
			ipv6 = a
		}
	}
	if e = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"provider_id": r.State.ServiceName, "public_ipv4": ipv4, "public_ipv6": ipv6}); e != nil {
		return nil, e
	}
	return dbGetInstance(ctx.AppDB(), inst.ID)
}

// Contract termination can require a provider email token. Tokens are used only
// for the immediate confirmation call and never stored in order state or logs.
func ovhContractDestroy(ctx *sdk.AppCtx, inst *Instance, token string) error {
	r, e := ovhOrderForInstance(ctx, inst)
	if e != nil {
		return e
	}
	if r.State.ServiceName == "" {
		kickOVHContract(ctx, inst.ID)
		return errors.New("OVH order delivery is unresolved; inventory retained to avoid abandoning a paid order")
	}
	unlock, e := ovhLockOrder(ctx, r.ID)
	if e != nil {
		return e
	}
	defer unlock()
	r, e = ovhOrderForInstance(ctx, inst)
	if e != nil {
		return e
	}
	product, _ := ovhSizeParts(inst.Size)
	prefix := "dedicated"
	if product == "vps" {
		prefix = "vps"
	}
	args := map[string]any{"serviceName": r.State.ServiceName}
	res, e := ctx.PlatformAPI().ExecuteIntegrationTool(r.ConnectionID, prefix+"_get", args)
	if e != nil {
		return e
	}
	if upstreamStatus(res) == 404 {
		if !r.State.InstallStarted && !r.State.TerminateStarted {
			return errors.New("OVH delivery is not yet visible; inventory retained")
		}
		return nil
	}
	if res == nil || !res.Success {
		return errors.New("OVH service existence could not be verified")
	}
	if !r.State.TerminateStarted {
		r.State.TerminateStarted = true
		r.State.Phase = "termination_confirmation"
		if e = ovhSaveOrder(ctx, r); e != nil {
			return e
		}
		if _, e = executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", prefix+"_terminate", args); e != nil {
			return errors.New("OVH termination request outcome is unknown; inspect the service and confirmation email before retrying")
		}
	}
	if token != "" {
		_, e = executeProviderToolOnConnection(ctx, r.ConnectionID, "ovhcloud", prefix+"_termination_confirm", map[string]any{"serviceName": r.State.ServiceName, "token": token})
		if e != nil {
			return errors.New("OVH termination confirmation failed; verify the token with OVH and retry")
		}
		r.State.TerminationConfirmed = true
		r.State.Phase = "termination_pending"
		if e = ovhSaveOrder(ctx, r); e != nil {
			return e
		}
	}
	_ = dbUpdateInstance(ctx.AppDB(), inst.ID, map[string]any{"lifecycle_stage": r.State.Phase})
	if !r.State.TerminationConfirmed {
		return errors.New("OVH cancellation requires the token emailed to the account administrator; use instance_order_status action=confirm_termination. Service and billing are not yet confirmed terminated")
	}
	return errors.New("OVH cancellation is confirmed but the service still exists; inventory remains until provider deletion is verified")
}
func (a *App) toolOrderStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	inst, e := dbGetInstance(ctx.AppDB(), int64Arg(args, "id"))
	if e != nil {
		return nil, e
	}
	if !isOVHContract(inst) {
		return nil, errors.New("instance is not an OVH contract product")
	}
	r, e := ovhOrderForInstance(ctx, inst)
	if e != nil {
		return nil, e
	}
	action := strArg(args, "action")
	if action != "" && action != "inspect" && action != "resume" && action != "confirm_termination" {
		return nil, errors.New("unsupported OVH order action")
	}
	if action == "confirm_termination" {
		if strArg(args, "token") == "" {
			return nil, errors.New("OVH termination email token is required")
		}
		if inst.Status != "destroying" {
			return nil, errors.New("request instance destruction before confirming cancellation")
		}
		if e = ovhContractDestroy(ctx, inst, strArg(args, "token")); e != nil {
			refreshed, readErr := ovhOrderForInstance(ctx, inst)
			if readErr != nil {
				return nil, readErr
			}
			r = refreshed
			return map[string]any{"order": ovhOrderPublic(r), "destroyed": false, "message": e.Error()}, nil
		}
		if e = resumeInstanceDestroy(ctx, inst); e != nil {
			return nil, e
		}
		return map[string]any{"destroyed": true}, nil
	} else if action == "resume" {
		if inst.Status == "destroying" && r.State.ServiceName != "" {
			if e = resumeInstanceDestroy(ctx, inst); e == nil {
				return map[string]any{"destroyed": true}, nil
			}
			return map[string]any{"order": ovhOrderPublic(r), "destroyed": false, "message": e.Error()}, nil
		}
		kickOVHContract(ctx, inst.ID)
	}
	r, e = ovhOrderForInstance(ctx, inst)
	if e != nil {
		return nil, e
	}
	return ovhOrderPublic(r), nil
}
func (a *App) handleOrderStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, 405, "POST only")
		return
	}
	args := map[string]any{}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&args); e != nil {
		httpErr(w, 400, "invalid order status request")
		return
	}
	v, e := a.toolOrderStatus(appCtxForRequest(r), args)
	if e != nil {
		httpProviderErr(w, e)
		return
	}
	httpJSON(w, v)
}
