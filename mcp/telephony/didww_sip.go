package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type didwwDIDRoute struct {
	ID           string
	TrunkID      string
	TrunkGroupID string
}

func findDIDWWDID(ctx *sdk.AppCtx, connectionID int64, number string) (didwwDIDRoute, error) {
	raw, err := executeCarrierTool(ctx, connectionID, "list_dids", map[string]any{"number": compactPhoneNumber(number), "page_size": 100})
	if err != nil {
		return didwwDIDRoute{}, err
	}
	var response struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Number     string `json:"number"`
				Terminated bool   `json:"terminated"`
			} `json:"attributes"`
			Relationships map[string]struct {
				Data *struct {
					ID string `json:"id"`
				} `json:"data"`
			} `json:"relationships"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return didwwDIDRoute{}, fmt.Errorf("decode DIDWW DID list: %w", err)
	}
	var found didwwDIDRoute
	for _, item := range response.Data {
		if item.Attributes.Terminated || compactPhoneNumber(item.Attributes.Number) != compactPhoneNumber(number) {
			continue
		}
		if item.ID == "" || found.ID != "" {
			return didwwDIDRoute{}, errors.New("DIDWW DID lookup is ambiguous or missing an ID")
		}
		found.ID = item.ID
		if relationship := item.Relationships["voice_in_trunk"].Data; relationship != nil {
			found.TrunkID = relationship.ID
		}
		if relationship := item.Relationships["voice_in_trunk_group"].Data; relationship != nil {
			found.TrunkGroupID = relationship.ID
		}
	}
	if found.ID == "" {
		return didwwDIDRoute{}, fmt.Errorf("DIDWW account does not own DID %s", number)
	}
	return found, nil
}

func didwwDIDTrunkDocument(didID, trunkID, groupID string) map[string]any {
	var trunk any
	if trunkID != "" {
		trunk = map[string]any{"type": "voice_in_trunks", "id": trunkID}
	}
	var group any
	if groupID != "" {
		group = map[string]any{"type": "voice_in_trunk_groups", "id": groupID}
	}
	return map[string]any{"data": map[string]any{
		"type": "dids", "id": didID,
		"relationships": map[string]any{
			"voice_in_trunk":       map[string]any{"data": trunk},
			"voice_in_trunk_group": map[string]any{"data": group},
		},
	}}
}

func setDIDWWDIDTrunk(ctx *sdk.AppCtx, connectionID int64, didID, trunkID, groupID string) error {
	_, err := executeCarrierTool(ctx, connectionID, "set_did_voice_trunk", map[string]any{
		"id": didID, "body": didwwDIDTrunkDocument(didID, trunkID, groupID),
	})
	return err
}

func restoreDIDWWDIDTrunk(ctx *sdk.AppCtx, connectionID int64, didID string, state directSIPProviderConfig) error {
	return setDIDWWDIDTrunk(ctx, connectionID, didID, state.PreviousTrunkID, state.PreviousTrunkGroupID)
}

func (a *App) configureDIDWWDirectSIP(ctx *sdk.AppCtx, route *routeRow, cfg sipGatewayConfig) error {
	if route == nil || route.PhoneNumber == "" || route.CarrierConnectionID == 0 {
		return errors.New("DIDWW route has no number or carrier connection")
	}
	if cfg.SRTPMode == sipSRTPRequired && cfg.Transport != "tls" {
		return errors.New("DIDWW direct SIP with required SRTP needs TLS signaling")
	}
	transportID := map[string]int{"udp": 1, "tcp": 2, "tls": 3}[strings.ToLower(cfg.Transport)]
	if transportID == 0 {
		return errors.New("unsupported DIDWW SIP transport")
	}
	_, portText, err := net.SplitHostPort(cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("DIDWW SIP gateway listen address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("DIDWW SIP gateway port is invalid")
	}
	did, err := findDIDWWDID(ctx, route.CarrierConnectionID, route.PhoneNumber)
	if err != nil {
		return err
	}
	mediaEncryption := "disabled"
	if cfg.SRTPMode != sipSRTPDisabled {
		mediaEncryption = "srtp_sdes"
	}
	raw, err := executeCarrierTool(ctx, route.CarrierConnectionID, "create_inbound_trunk", map[string]any{
		"name":     "Apteva " + route.PhoneNumber,
		"priority": 1, "weight": 100,
		"configuration": map[string]any{"type": "sip_configurations", "attributes": map[string]any{
			"username": route.PhoneNumber, "host": cfg.PublicHost, "port": port,
			"codec_ids": []int{9, 10}, "transport_protocol_id": transportID,
			"media_encryption_mode": mediaEncryption,
		}},
	})
	if err != nil {
		return fmt.Errorf("create DIDWW inbound trunk: %w", err)
	}
	trunkID := providerResourceID(raw)
	if trunkID == "" {
		return errors.New("DIDWW inbound trunk response contained no ID")
	}
	rollback := func() {
		_, _ = executeCarrierTool(ctx, route.CarrierConnectionID, "delete_inbound_trunk", map[string]any{"id": trunkID})
	}
	if err := setDIDWWDIDTrunk(ctx, route.CarrierConnectionID, did.ID, trunkID, ""); err != nil {
		rollback()
		return fmt.Errorf("assign DIDWW DID to SIP trunk: %w", err)
	}
	state := directSIPProviderConfig{Provider: "didww", TrunkID: trunkID,
		PreviousTrunkID: did.TrunkID, PreviousTrunkGroupID: did.TrunkGroupID}
	encoded, _ := json.Marshal(state)
	if err := a.db().updateRoutePhoneSID(route.ID, did.ID); err != nil {
		_ = restoreDIDWWDIDTrunk(ctx, route.CarrierConnectionID, did.ID, state)
		rollback()
		return err
	}
	if err := a.db().updateRouteTransport(route.ID, inboundTransportSIPDirect, string(encoded)); err != nil {
		_ = restoreDIDWWDIDTrunk(ctx, route.CarrierConnectionID, did.ID, state)
		rollback()
		return err
	}
	route.PhoneNumberSID = did.ID
	route.TransportConfig = string(encoded)
	return nil
}
