package main

import (
	"fmt"
	"strings"
)

// inboundCarrierCapabilities is the contract between the provider-independent
// routing graph and the transport executing it. A route may only publish nodes
// whose effects its inbound transport can actually deliver to the caller.
type inboundCarrierCapabilities struct {
	Decisions             bool
	DTMFMenu              bool
	Voicemail             bool
	ExternalDestinations  bool
	TerminalAnnouncements bool
}

func inboundCapabilities(route *routeRow) inboundCarrierCapabilities {
	if route == nil {
		return inboundCarrierCapabilities{}
	}
	if route.InboundTransport == inboundTransportSIPDirect {
		// The local SIP dialog can stay ringing through a decision loop and can
		// be claimed by exactly one browser adviser or answered by Core. It has
		// no independent speech, digit-gather, or recording engine yet.
		switch route.CarrierSlug {
		case "twilio", "telnyx", "didww":
			return inboundCarrierCapabilities{Decisions: true}
		default:
			return inboundCarrierCapabilities{}
		}
	}
	switch route.CarrierSlug {
	case "twilio":
		return inboundCarrierCapabilities{Decisions: true, DTMFMenu: true, Voicemail: true, ExternalDestinations: true, TerminalAnnouncements: true}
	case "telnyx":
		return inboundCarrierCapabilities{Decisions: true, DTMFMenu: true, ExternalDestinations: true, TerminalAnnouncements: true}
	case "bandwidth":
		return inboundCarrierCapabilities{Decisions: true, TerminalAnnouncements: true}
	case "plivo":
		return inboundCarrierCapabilities{ExternalDestinations: true}
	default:
		return inboundCarrierCapabilities{}
	}
}

// Previously published flows may predate route capability validation. Check
// them again at ingress so an unsupported media node cannot be skipped when a
// direct SIP call reaches a terminal node.
func (a *App) validatePublishedFlowForInboundRoute(route *routeRow) error {
	if route == nil || route.PublishedFlowVersionID == "" {
		return nil
	}
	version, err := a.findRoutingVersion(route.ProjectID, route.PublishedFlowVersionID)
	if err != nil {
		return err
	}
	if version == nil {
		return fmt.Errorf("published routing version %s is unavailable", route.PublishedFlowVersionID)
	}
	definition, err := parseRoutingDefinition(version.Definition)
	if err != nil {
		return err
	}
	if problems := a.validateFlowForRoute(route.ProjectID, definition, route); len(problems) != 0 {
		return fmt.Errorf("published flow cannot run on %s %s: %s", route.CarrierSlug, route.InboundTransport, strings.Join(problems, "; "))
	}
	return nil
}
