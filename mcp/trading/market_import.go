package main

import (
	"bytes"
	"encoding/json"
	"strings"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/trading/internal/marketdata"
)

func (a *App) toolMarketDataImport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	// The export is supplied as data. No URL is fetched and no local path is read
	// by the sidecar. Portable artifacts capture the normalized observations.
	requestArgs := map[string]any{}
	for key, value := range args {
		// Apteva attaches trusted call metadata outside the advertised schema.
		if !strings.HasPrefix(key, "_") {
			requestArgs[key] = value
		}
	}
	raw, err := json.Marshal(requestArgs)
	if err != nil {
		return nil, err
	}
	var req marketdata.Request
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return nil, err
	}
	return marketdata.Import(req)
}
