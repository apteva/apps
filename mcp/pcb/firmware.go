package main

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

const firmwareSchema = "apteva-pcb-firmware-run/v1"

type FirmwareOptions struct {
	Source           string             `json:"source"`
	Language         string             `json:"language,omitempty"`
	Board            string             `json:"board,omitempty"`
	Iterations       int                `json:"iterations,omitempty"`
	ExecutorFunction string             `json:"executor_function,omitempty"`
	SensorValues     map[string]float64 `json:"sensor_values,omitempty"`
	// DigitalInputs and AnalogInputs drive digitalRead and analogRead by pin
	// (number or name such as A0). Analog values are raw ADC counts.
	DigitalInputs map[string]int     `json:"digital_inputs,omitempty"`
	AnalogInputs  map[string]float64 `json:"analog_inputs,omitempty"`
	// I2CReadData is what each I2C address returns to Wire.requestFrom, by
	// address ("0x44" or "68"); the stream is consumed in order and cycles.
	I2CReadData map[string][]int `json:"i2c_read_data,omitempty"`
	// WiFi is "connected" (default) or "unavailable" for WiFi.status().
	WiFi string `json:"wifi,omitempty"`
}

func (o FirmwareOptions) WiFiAvailable() bool {
	return !strings.EqualFold(strings.TrimSpace(o.WiFi), "unavailable")
}

func (o FirmwareOptions) i2cData(address int) []int {
	for key, data := range o.I2CReadData {
		if parseI2CAddressKey(key) == address {
			return data
		}
	}
	return nil
}

func (o FirmwareOptions) hasI2CData(address int) bool { return o.i2cData(address) != nil }

func parseI2CAddressKey(key string) int {
	key = strings.TrimSpace(strings.ToLower(key))
	base := 10
	if strings.HasPrefix(key, "0x") {
		key, base = key[2:], 16
	}
	v, err := strconv.ParseInt(key, base, 32)
	if err != nil {
		return -1
	}
	return int(v)
}

// FirmwareSpec stores a board's firmware in the revision so the readiness
// gate can run it. ExpectSerial lists substrings the serial output must
// contain for the run to count as passing.
type FirmwareSpec struct {
	Board         string             `json:"board,omitempty"`
	Source        string             `json:"source"`
	Iterations    int                `json:"iterations,omitempty"`
	SensorValues  map[string]float64 `json:"sensor_values,omitempty"`
	DigitalInputs map[string]int     `json:"digital_inputs,omitempty"`
	AnalogInputs  map[string]float64 `json:"analog_inputs,omitempty"`
	I2CReadData   map[string][]int   `json:"i2c_read_data,omitempty"`
	WiFi          string             `json:"wifi,omitempty"`
	ExpectSerial  []string           `json:"expect_serial,omitempty"`
}

const maxFirmwareSourceBytes = 256 * 1024

type I2CTransaction struct {
	TimeUS    int64  `json:"time_us"`
	Address   int    `json:"address"`
	Bytes     []int  `json:"bytes"`
	Status    string `json:"status"`
	Direction string `json:"direction,omitempty"` // write (default) | read
}

type FirmwareRunResult struct {
	Schema          string             `json:"schema"`
	Engine          string             `json:"engine"`
	Status          string             `json:"status"`
	Language        string             `json:"language"`
	Board           string             `json:"board"`
	Runtime         string             `json:"runtime"`
	Iterations      int                `json:"iterations"`
	DurationUS      int64              `json:"duration_us"`
	SerialBaud      int                `json:"serial_baud,omitempty"`
	SerialOutput    []string           `json:"serial_output"`
	PinModes        map[string]string  `json:"pin_modes"`
	PinStates       map[string]string  `json:"pin_states"`
	I2CTransactions []I2CTransaction   `json:"i2c_transactions"`
	VirtualDevices  []map[string]any   `json:"virtual_devices,omitempty"`
	Variables       map[string]float64 `json:"variables,omitempty"`
	Warnings        []string           `json:"warnings,omitempty"`
	Executor        map[string]any     `json:"executor,omitempty"`
	// Unsupported counts statements and calls the behavioral runtime could not
	// execute. A run with unsupported code still completes but cannot pass
	// the firmware readiness gate.
	Unsupported int `json:"unsupported"`
	Statements  int `json:"statements"`
	SensorReads int `json:"sensor_reads"`
	InputReads  int `json:"input_reads"`
	// DeepSleep is set when the sketch entered ESP32 deep sleep; the run
	// stops there, like the chip. SleepWakeUS is the armed timer wakeup.
	DeepSleep   bool  `json:"deep_sleep,omitempty"`
	SleepWakeUS int64 `json:"sleep_wake_us,omitempty"`
	Restarted   bool  `json:"restarted,omitempty"`
}

func extractFirmwareFunction(source, name string) (string, bool) {
	pattern := regexp.MustCompile(`(?m)\b(?:void\s+)?` + regexp.QuoteMeta(name) + `\s*\([^)]*\)\s*\{`)
	location := pattern.FindStringIndex(source)
	if location == nil {
		return "", false
	}
	start, depth := location[1], 1
	for i := start; i < len(source); i++ {
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return source[start:i], true
			}
		}
	}
	return "", false
}

func stripFirmwareComments(source string) string {
	block := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(source, "")
	line := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(block, "")
	return line
}

func splitFirmwareStatements(body string) []string {
	out, start, quoted, escaped := []string{}, 0, byte(0), false
	for i := 0; i < len(body); i++ {
		char := body[i]
		if quoted != 0 {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == quoted {
				quoted = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			quoted = char
			continue
		}
		if char == ';' {
			out = append(out, body[start:i])
			start = i + 1
		}
	}
	if strings.TrimSpace(body[start:]) != "" {
		out = append(out, body[start:])
	}
	return out
}

func splitFirmwareArgs(body string) []string {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	out, start, quoted, depth := []string{}, 0, byte(0), 0
	for i := 0; i < len(body); i++ {
		char := body[i]
		if quoted != 0 {
			if char == quoted && (i == 0 || body[i-1] != '\\') {
				quoted = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			quoted = char
			continue
		}
		if char == '(' {
			depth++
		}
		if char == ')' {
			depth--
		}
		if char == ',' && depth == 0 {
			out = append(out, strings.TrimSpace(body[start:i]))
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(body[start:]))
}

func firmwarePin(value string) string { return strings.Trim(strings.TrimSpace(value), `"'`) }
func virtualI2CAddressPresent(devices []map[string]any, address int) bool {
	for _, device := range devices {
		if device["kind"] != "i2c_sensor" {
			continue
		}
		switch value := device["address"].(type) {
		case int:
			if value == address {
				return true
			}
		case float64:
			if int(value) == address {
				return true
			}
		}
	}
	return false
}

func inferFirmwareBoard(def *Definition) string {
	for _, component := range def.Components {
		value := strings.ToLower(component.Name + " " + component.Value)
		switch {
		case strings.Contains(value, "esp32-c3"):
			return "esp32c3"
		case strings.Contains(value, "esp32-s3"):
			return "esp32s3"
		case strings.Contains(value, "esp32-c6"):
			return "esp32c6"
		case strings.Contains(value, "esp32"):
			return "esp32"
		case strings.Contains(value, "rp2040"):
			return "rp2040"
		case strings.Contains(value, "atmega328") || strings.Contains(value, "arduino uno"):
			return "arduino:avr:uno"
		}
	}
	return "generic-arduino"
}

func firmwareResultJSON(result *FirmwareRunResult) []byte {
	body, _ := json.MarshalIndent(result, "", "  ")
	return append(body, '\n')
}
