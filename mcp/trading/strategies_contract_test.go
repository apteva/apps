package main

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// Strategies owns trading_contract.json: the Trading tools and order_place
// fields it depends on. Trading must keep providing every one of them.
func TestStrategiesContractIsProvided(t *testing.T) {
	raw, err := os.ReadFile("../strategies/trading_contract.json")
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("Strategies source not present next to Trading")
	}
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		AppOnlyTools     []string `json:"app_only_tools"`
		Tools            []string `json:"tools"`
		OrderPlaceFields []string `json:"order_place_fields"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	tools := map[string]sdk.Tool{}
	for _, tool := range (&App{}).MCPTools() {
		tools[tool.Name] = tool
	}
	for _, name := range contract.Tools {
		if _, ok := tools[name]; !ok {
			t.Errorf("Strategies calls %s, which Trading no longer provides", name)
		}
	}
	for _, name := range contract.AppOnlyTools {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("Strategies calls app-only %s, which Trading no longer provides", name)
			continue
		}
		if tool.Exposure != sdk.ToolExposureAppOnly {
			t.Errorf("%s must stay app-only; exposure is %q", name, tool.Exposure)
		}
	}
	order, ok := tools["order_place"]
	if !ok {
		t.Fatal("order_place missing")
	}
	props, _ := order.InputSchema["properties"].(map[string]any)
	for _, field := range contract.OrderPlaceFields {
		if _, ok := props[field]; !ok {
			t.Errorf("Strategies forwards order_place.%s, which Trading's schema no longer accepts", field)
		}
	}
}
