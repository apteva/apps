package main

// One stable key across carrier retries lets projections upsert a single
// callback opportunity for an unhandled call.
func callbackOpportunityID(call callRow) string {
	if !callbackEligible(call) {
		return ""
	}
	return "callback:" + call.ID
}

func callbackEligible(call callRow) bool {
	if call.Direction != "inbound" || !isTerminalStatus(call.Status) || call.HandlingReason != "" {
		return false
	}
	if call.MediaConnectedAt == "" {
		return true
	}
	return call.PeerKind == peerKindRealtime && call.CallbackOnAI
}

func callClassification(call callRow) string {
	if call.HandlingReason != "" {
		return call.HandlingReason
	}
	if call.MediaConnectedAt != "" {
		if call.PeerKind == peerKindHuman {
			return "human_connected"
		}
		if call.PeerKind == peerKindRealtime {
			return "ai_handled"
		}
		return "connected"
	}
	if !isTerminalStatus(call.Status) {
		return "routing"
	}
	if call.TerminationInitiator == "caller" || call.Status == "canceled" {
		return "caller_abandoned"
	}
	if call.RoutingResolution != "" {
		return call.RoutingResolution
	}
	return "unhandled"
}
