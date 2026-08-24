package main

import (
	"math"
	"reflect"
	"testing"
)

func TestSensorNodeSimulationPowerAndI2C(t *testing.T) {
	def := sensorNodeExample()
	result, err := simulateDefinition(&def, SimulationOptions{
		DurationUS: 2_000,
		StepUS:     100,
		Sources:    []SimulationSource{{ID: "usb", NetID: "usb5v", Kind: "dc", Value: 5}},
		Probes: []SimulationProbe{
			{ID: "usb", NetID: "usb5v", Kind: "voltage"},
			{ID: "rail", NetID: "v3v3", Kind: "voltage"},
			{ID: "sda", NetID: "i2c_sda", Kind: "digital"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.FinalVoltage["usb5v"]-5) > 1e-6 {
		t.Fatalf("usb5v=%v", result.FinalVoltage["usb5v"])
	}
	if math.Abs(result.FinalVoltage["v3v3"]-3.3) > 1e-6 {
		t.Fatalf("v3v3=%v", result.FinalVoltage["v3v3"])
	}
	if result.FinalDigital["i2c_sda"] != "high" || result.FinalDigital["i2c_scl"] != "high" {
		t.Fatalf("I2C pullups not simulated: sda=%s scl=%s", result.FinalDigital["i2c_sda"], result.FinalDigital["i2c_scl"])
	}
	foundPoweredSensor := false
	for _, device := range result.DeviceStates {
		if device["kind"] == "i2c_sensor" && device["powered"] == true {
			foundPoweredSensor = true
		}
	}
	if !foundPoweredSensor {
		t.Fatalf("powered I2C sensor missing: %#v", result.DeviceStates)
	}
	second, err := simulateDefinition(&def, SimulationOptions{DurationUS: 2_000, StepUS: 100, Sources: []SimulationSource{{ID: "usb", NetID: "usb5v", Kind: "dc", Value: 5}}, Probes: []SimulationProbe{{ID: "rail", NetID: "v3v3", Kind: "voltage"}}})
	if err != nil {
		t.Fatal(err)
	}
	third, err := simulateDefinition(&def, SimulationOptions{DurationUS: 2_000, StepUS: 100, Sources: []SimulationSource{{ID: "usb", NetID: "usb5v", Kind: "dc", Value: 5}}, Probes: []SimulationProbe{{ID: "rail", NetID: "v3v3", Kind: "voltage"}}})
	if err != nil || !reflect.DeepEqual(second, third) {
		t.Fatal("simulation is not deterministic")
	}
}

func TestSimulationClockWaveform(t *testing.T) {
	def := emptyDefinition("clock")
	def.Nets = []Net{{ID: "gnd", Name: "GND"}, {ID: "clk", Name: "CLK"}}
	result, err := simulateDefinition(&def, SimulationOptions{DurationUS: 400, StepUS: 100, Sources: []SimulationSource{{ID: "clock", NetID: "clk", Kind: "clock", Value: 3.3, PeriodUS: 200, DutyCycle: .5}}, Probes: []SimulationProbe{{ID: "clk", NetID: "clk", Kind: "digital"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"high", "low", "high", "low", "high"}
	got := []string{}
	for _, point := range result.Waveforms[0].Points {
		got = append(got, point.Digital)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("clock=%v want %v", got, want)
	}
}
