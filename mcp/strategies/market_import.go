package main

import (
	"bytes"
	"encoding/json"
	"strings"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/strategies/internal/marketdata"
)

func (a *App) toolMarketDataImport(ctx *sdk.AppCtx, args map[string]any) (any, error) {

	requestArgs := map[string]any{}
	for key, value := range args {

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
