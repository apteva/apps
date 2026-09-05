package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type observedVolume struct {
	ID, State string
	SizeGB    int
	Attached  []string
	Ready     bool
}
type volumeIntent struct {
	Tool               string
	Args               map[string]any
	InstanceID         int64
	ProviderInstanceID string
	SizeGB             int
}

func readProviderVolume(ctx *sdk.AppCtx, v *InstanceVolume) (*observedVolume, error) {
	tool := ""
	args := map[string]any{}
	switch v.Provider {
	case "hetzner":
		tool = "volume_get"
		args["id"] = atoiAny(v.ProviderVolumeID)
	case "digitalocean":
		tool = "volume_get"
		args["volume_id"] = v.ProviderVolumeID
	case "vultr":
		tool = "block_storage_get"
		args["block_id"] = v.ProviderVolumeID
	case "scaleway":
		tool = "volume_get"
		if v.StorageClass == "local" {
			tool = "instance_volume_get"
		}
		args["zone"] = v.Region
		args["volume_id"] = v.ProviderVolumeID
	case "aws-ec2":
		tool = "volume_list"
		args = map[string]any{"Action": "DescribeVolumes", "Version": "2016-11-15", "VolumeId.1": v.ProviderVolumeID}
	case "huawei-cloud":
		tool = "get_volume"
		args["volume_id"] = v.ProviderVolumeID
	case "linode", "ovhcloud":
		tool = "get_volume"
		args["volumeId"] = atoiAny(v.ProviderVolumeID)
		if v.Provider == "ovhcloud" {
			args["volumeId"] = v.ProviderVolumeID
		}
	case "runpod":
		tool = "get_network_volume"
		args["networkVolumeId"] = v.ProviderVolumeID
	default:
		return nil, providerAdapterUnavailable(v.Provider, "volume verification")
	}
	data, err := executeVolumeTool(ctx, v.ProviderConnectionID, v.Provider, tool, args)
	if err != nil {
		if strings.Contains(err.Error(), "status=404") {
			return nil, ErrVolumeNotFound
		}
		return nil, err
	}
	return parseObservedVolume(v, data)
}

func parseObservedVolume(v *InstanceVolume, data json.RawMessage) (*observedVolume, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	obj := root
	for _, key := range []string{"volume", "block_storage", "networkVolume"} {
		if nested, ok := root[key].(map[string]any); ok {
			obj = nested
			break
		}
	}
	if v.Provider == "aws-ec2" {
		obj = nil
		for _, candidate := range collectMaps(root) {
			if mapString(candidate, "volumeId") == v.ProviderVolumeID {
				obj = candidate
				break
			}
		}
		if obj == nil {
			return nil, ErrVolumeNotFound
		}
	}
	id := mapString(obj, "id")
	if v.Provider == "aws-ec2" {
		id = mapString(obj, "volumeId")
	}
	if id != v.ProviderVolumeID {
		return nil, fmt.Errorf("provider volume identity mismatch: got %q, want %q", id, v.ProviderVolumeID)
	}
	state := strings.ToLower(firstNonEmpty(mapString(obj, "status"), mapString(obj, "state")))
	result := &observedVolume{ID: id, State: state, SizeGB: anyToInt(mapValue(obj, "size")), Attached: []string{}}
	if v.Provider == "digitalocean" {
		result.SizeGB = anyToInt(obj["size_gigabytes"])
	}
	if v.Provider == "vultr" {
		result.SizeGB = anyToInt(obj["size_gb"])
	}
	if v.Provider == "scaleway" {
		result.SizeGB = anyToInt(obj["size"]) / 1000000000
	}
	result.Ready = state == "available" || state == "active" || state == "in-use" || state == "in_use" || (state == "" && (v.Provider == "digitalocean" || v.Provider == "runpod"))
	add := func(value string) {
		if value != "" && value != "0" {
			result.Attached = append(result.Attached, value)
		}
	}
	switch v.Provider {
	case "hetzner":
		add(mapString(obj, "server"))
	case "digitalocean":
		for _, id := range mapStringSlice(obj, "droplet_ids") {
			add(id)
		}
	case "vultr":
		add(mapString(obj, "attached_to_instance"))
	case "linode":
		add(mapString(obj, "linode_id"))
		if readiness, ok := obj["readiness"].(map[string]any); ok {
			if ready, ok := readiness["is_ready"].(bool); ok {
				result.Ready = result.Ready && ready
			}
		}
	case "ovhcloud":
		for _, id := range mapStringSlice(obj, "attachedTo") {
			add(id)
		}
	case "scaleway":
		for _, ref := range collectMaps(obj["references"]) {
			if mapString(ref, "product_resource_type") == "instance" || strings.Contains(mapString(ref, "product_resource_type"), "server") {
				add(mapString(ref, "product_resource_id"))
				if status := mapString(ref, "status"); status != "attached" && status != "detached" {
					result.Ready = false
				}
			}
		}
	case "huawei-cloud":
		for _, ref := range collectMaps(obj["attachments"]) {
			add(mapString(ref, "server_id"))
		}
	case "aws-ec2":
		for _, ref := range collectMaps(obj["attachmentSet"]) {
			add(mapString(ref, "instanceId"))
			if status := mapString(ref, "status"); status != "" && status != "attached" {
				result.Ready = false
			}
		}
	}
	if state == "error" || strings.HasPrefix(state, "error_") {
		return nil, fmt.Errorf("provider volume %s entered %s", id, state)
	}
	return result, nil
}

func volumeConverged(observed *observedVolume, operation string, intent volumeIntent) bool {
	if observed == nil || !observed.Ready {
		return false
	}
	switch operation {
	case "attach":
		return len(observed.Attached) == 1 && observed.Attached[0] == intent.ProviderInstanceID
	case "detach":
		return len(observed.Attached) == 0
	case "resize":
		return observed.SizeGB >= intent.SizeGB
	case "create":
		return observed.SizeGB >= intent.SizeGB && (intent.ProviderInstanceID == "" || containsString(observed.Attached, intent.ProviderInstanceID))
	}
	return false
}

func persistVolumeIntent(db *sql.DB, id int64, operation string, intent volumeIntent) error {
	input, _ := json.Marshal(intent)
	var previous, payload string
	err := db.QueryRow(`SELECT operation,input_json FROM resource_operations WHERE resource_kind='volume' AND resource_id=?`, id).Scan(&previous, &payload)
	if err == nil {
		if previous == "await_attach" && (operation == "attach" || operation == "delete") {
			var pending volumeIntent
			if err = json.Unmarshal([]byte(payload), &pending); err != nil {
				return err
			}
			if operation == "attach" && pending.InstanceID != intent.InstanceID {
				return errors.New("pending volume targets another instance")
			}
			_, err = db.Exec(`UPDATE resource_operations SET operation=?,input_json=?,error='' WHERE resource_kind='volume' AND resource_id=?`, operation, string(input), id)
			return err
		}
		if previous != operation || payload != string(input) {
			return errors.New("a different volume operation must be reconciled first")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = db.Exec(`INSERT INTO resource_operations(resource_kind,resource_id,token,operation,input_json) VALUES ('volume',?,?,?,?)`, id, newRequestID(), operation, string(input))
	return err
}

func verifyVolumeOperation(ctx *sdk.AppCtx, v *InstanceVolume, operation string, intent volumeIntent) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		observed, err := readProviderVolume(ctx, v)
		if operation == "delete" && errors.Is(err, ErrVolumeNotFound) {
			return completeVolumeOperation(ctx, v, operation, intent)
		}
		if err != nil {
			return err
		}
		if volumeConverged(observed, operation, intent) {
			return completeVolumeOperation(ctx, v, operation, intent)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("volume %d %s is still pending; provider state=%s", v.ID, operation, observed.State)
		}
		if err = sleepOperation(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

func completeVolumeOperation(ctx *sdk.AppCtx, v *InstanceVolume, operation string, intent volumeIntent) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	switch operation {
	case "delete":
		_, err = tx.Exec(`DELETE FROM instance_volumes WHERE id=?`, v.ID)
	case "attach":
		_, err = tx.Exec(`UPDATE instance_volumes SET instance_id=?,status='attached',error_message='' WHERE id=?`, intent.InstanceID, v.ID)
	case "detach":
		_, err = tx.Exec(`UPDATE instance_volumes SET instance_id=CASE WHEN delete_policy='with_instance' AND instance_id IN(SELECT id FROM instances WHERE status IN('destroying','rolling_back')) THEN instance_id ELSE NULL END,status='available',mount_path='',device_path='',provider_device_path='',error_message='' WHERE id=?`, v.ID)
	case "resize":
		_, err = tx.Exec(`UPDATE instance_volumes SET size_gb=?,status=CASE WHEN instance_id IS NULL THEN 'available' ELSE 'attached' END,error_message='' WHERE id=?`, intent.SizeGB, v.ID)
	case "create":
		_, err = tx.Exec(`UPDATE instance_volumes SET instance_id=CASE WHEN ? <> '' THEN ? ELSE instance_id END,status=CASE WHEN ? <> '' THEN 'attached' ELSE 'available' END,error_message='' WHERE id=?`, intent.ProviderInstanceID, intent.InstanceID, intent.ProviderInstanceID, v.ID)
	}
	if err != nil {
		return err
	}
	if operation == "create" && intent.InstanceID > 0 && intent.ProviderInstanceID == "" {
		_, err = tx.Exec(`UPDATE resource_operations SET operation='await_attach',error='' WHERE resource_kind='volume' AND resource_id=?`, v.ID)
	} else {
		_, err = tx.Exec(`DELETE FROM resource_operations WHERE resource_kind='volume' AND resource_id=?`, v.ID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func mutateProviderVolume(ctx *sdk.AppCtx, v *InstanceVolume, operation, tool string, args map[string]any, intent volumeIntent) (err error) {
	if strings.HasPrefix(v.ProviderVolumeID, "pending:") {
		return errors.New("volume create outcome is unknown; reconcile its provider identity before another mutation")
	}
	intent.Tool, intent.Args = tool, args
	var pending int
	var previousOperation string
	_ = ctx.AppDB().QueryRow("SELECT operation FROM resource_operations WHERE resource_kind='volume' AND resource_id=?", v.ID).Scan(&previousOperation)
	_ = ctx.AppDB().QueryRow("SELECT COUNT(*) FROM resource_operations WHERE resource_kind='volume' AND resource_id=?", v.ID).Scan(&pending)
	if err = persistVolumeIntent(ctx.AppDB(), v.ID, operation, intent); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_, _ = ctx.AppDB().Exec(`UPDATE resource_operations SET error=?,updated_at=CURRENT_TIMESTAMP WHERE resource_kind='volume' AND resource_id=?`, err.Error(), v.ID)
			_ = dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"error_message": err.Error()})
		}
	}()
	status := map[string]string{"attach": "attaching", "detach": "detaching", "resize": "resizing", "delete": "deleting"}[operation]
	if err = dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"status": status}); err != nil {
		return err
	}
	if pending > 0 && previousOperation != "await_attach" {
		observed, observeErr := readProviderVolume(ctx, v)
		if (operation == "delete" && errors.Is(observeErr, ErrVolumeNotFound)) || (observeErr == nil && volumeConverged(observed, operation, intent)) {
			return completeVolumeOperation(ctx, v, operation, intent)
		}
		if observeErr != nil {
			return observeErr
		}
		if !observed.Ready {
			return verifyVolumeOperation(ctx, v, operation, intent)
		}
	}
	// These operations have a fixed immutable resource/target and are safe to
	// resume. A provider conflict may mean a previous accepted request is running.
	_, err = executeVolumeTool(ctx, v.ProviderConnectionID, v.Provider, tool, args)
	if err != nil && !(operation == "delete" && strings.Contains(err.Error(), "status=404")) && !strings.Contains(err.Error(), "status=409") {
		if strings.Contains(err.Error(), "status=400") || strings.Contains(err.Error(), "status=401") || strings.Contains(err.Error(), "status=403") || strings.Contains(err.Error(), "status=422") {
			_, _ = ctx.AppDB().Exec(`DELETE FROM resource_operations WHERE resource_kind='volume' AND resource_id=?`, v.ID)
			_ = dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"status": map[bool]string{true: "attached", false: "available"}[v.InstanceID != nil]})
		}
		return err
	}
	return verifyVolumeOperation(ctx, v, operation, intent)
}

func reconcileVolumeOperations(ctx *sdk.AppCtx) {
	rows, err := ctx.AppDB().Query(`SELECT resource_id,operation,input_json FROM resource_operations WHERE resource_kind='volume'`)
	if err != nil {
		return
	}
	type pending struct {
		id          int64
		kind, input string
	}
	all := []pending{}
	for rows.Next() {
		var p pending
		if rows.Scan(&p.id, &p.kind, &p.input) == nil {
			all = append(all, p)
		}
	}
	rows.Close()
	for _, p := range all {
		unlock, err := lockResource(ctx.AppDB(), "volume", p.id)
		if err != nil {
			continue
		}
		v, err := dbGetVolume(ctx.AppDB(), p.id)
		if err == nil {
			var intent volumeIntent
			err = json.Unmarshal([]byte(p.input), &intent)
			if err == nil && !strings.HasPrefix(v.ProviderVolumeID, "pending:") {
				if p.kind == "await_attach" {
					inst, e := dbGetInstance(ctx.AppDB(), intent.InstanceID)
					if e != nil {
						err = e
					} else if inst.Status == "ready" {
						unlockInstance, e := lockResource(ctx.AppDB(), "instance", inst.ID)
						if e != nil {
							err = e
						} else {
							err = attachProviderVolume(ctx, v, inst)
							unlockInstance()
						}
					} else {
						err = fmt.Errorf("attachment waiting for instance %d lifecycle (%s)", inst.ID, inst.Status)
					}
				} else if intent.Tool != "" {
					err = mutateProviderVolume(ctx, v, p.kind, intent.Tool, intent.Args, intent)
				} else {
					err = verifyVolumeOperation(ctx, v, p.kind, intent)
				}
				if err == nil && p.kind == "create" && intent.InstanceID > 0 {
					inst, e := dbGetInstance(ctx.AppDB(), intent.InstanceID)
					if e == nil && inst.Status == "ready" {
						err = attachCreatedVolume(ctx, v, inst)
					}
				}
			}
			if strings.HasPrefix(v.ProviderVolumeID, "pending:") {
				err = errors.New("create outcome unknown: reconcile with the recorded provider account before retrying; no duplicate was created")
			}
			if err != nil {
				_ = dbUpdateVolume(ctx.AppDB(), v.ID, map[string]any{"error_message": "recovery: " + err.Error()})
			}
		}
		unlock()
	}
}
