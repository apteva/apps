package main

import (
	"encoding/json"
	"fmt"
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
}

type I2CTransaction struct {
	TimeUS  int64  `json:"time_us"`
	Address int    `json:"address"`
	Bytes   []int  `json:"bytes"`
	Status  string `json:"status"`
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
}

var (
	firmwareCallPattern   = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.]*)\s*\((.*)\)$`)
	firmwareAssignPattern = regexp.MustCompile(`^(?:(?:const|float|double|int|long|bool|auto)\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.+)$`)
)

func runFirmwareLab(def *Definition, options FirmwareOptions) (*FirmwareRunResult, error) {
	if strings.TrimSpace(options.Source) == "" {
		return nil, fmt.Errorf("firmware source required")
	}
	if options.Language == "" {
		options.Language = "arduino"
	}
	if strings.ToLower(options.Language) != "arduino" && strings.ToLower(options.Language) != "cpp" {
		return nil, fmt.Errorf("v0.5 firmware lab supports Arduino-compatible C++")
	}
	if options.Board == "" {
		options.Board = inferFirmwareBoard(def)
	}
	if options.Iterations <= 0 {
		options.Iterations = 1
	}
	if options.Iterations > 100 {
		return nil, fmt.Errorf("iterations must not exceed 100")
	}
	values := map[string]float64{"temperature": 23.5, "temperature_c": 23.5, "humidity": 45, "humidity_percent": 45}
	for key, value := range options.SensorValues {
		values[strings.ToLower(key)] = value
	}
	result := &FirmwareRunResult{Schema: firmwareSchema, Engine: engineVersion, Status: "passed", Language: "arduino", Board: options.Board, Runtime: "apteva-arduino-behavioral/0.5", Iterations: options.Iterations, SerialOutput: []string{}, PinModes: map[string]string{}, PinStates: map[string]string{}, I2CTransactions: []I2CTransaction{}, Variables: map[string]float64{}, Warnings: []string{}}
	_, _, _, devices := compileSimulationModel(def, nil)
	result.VirtualDevices = devices
	setup, setupOK := extractFirmwareFunction(options.Source, "setup")
	loop, loopOK := extractFirmwareFunction(options.Source, "loop")
	if !setupOK && !loopOK {
		return nil, fmt.Errorf("Arduino source must define setup() or loop()")
	}
	state := &firmwareRuntimeState{result: result, variables: values, i2cAddress: -1, i2cBytes: []int{}}
	if setupOK {
		executeFirmwareStatements(setup, state)
	}
	for iteration := 0; iteration < options.Iterations; iteration++ {
		if loopOK {
			executeFirmwareStatements(loop, state)
		}
	}
	for key, value := range state.variables {
		if key != "temperature" && key != "temperature_c" && key != "humidity" && key != "humidity_percent" {
			result.Variables[key] = roundFloat(value, 6)
		}
	}
	return result, nil
}

type firmwareRuntimeState struct {
	result     *FirmwareRunResult
	variables  map[string]float64
	serialLine string
	i2cAddress int
	i2cBytes   []int
}

func executeFirmwareStatements(body string, state *firmwareRuntimeState) {
	for _, statement := range splitFirmwareStatements(stripFirmwareComments(body)) {
		statement = strings.TrimSpace(statement)
		if statement == "" || strings.HasPrefix(statement, "return ") {
			continue
		}
		if match := firmwareAssignPattern.FindStringSubmatch(statement); len(match) == 3 {
			state.variables[match[1]] = firmwareExpressionValue(match[2], state)
			continue
		}
		match := firmwareCallPattern.FindStringSubmatch(statement)
		if len(match) != 3 {
			state.result.Warnings = appendUnique(state.result.Warnings, "unsupported statement: "+shortLabel(statement, 80))
			continue
		}
		call := match[1]
		args := splitFirmwareArgs(match[2])
		switch call {
		case "Serial.begin":
			state.result.SerialBaud = int(firmwareArgNumber(args, 0, state))
		case "Serial.print", "Serial.println":
			value := firmwarePrintable(args, state)
			state.serialLine += value
			if call == "Serial.println" {
				state.result.SerialOutput = append(state.result.SerialOutput, state.serialLine)
				state.serialLine = ""
			}
		case "pinMode":
			if len(args) >= 2 {
				state.result.PinModes[firmwarePin(args[0])] = strings.ToUpper(strings.TrimSpace(args[1]))
			}
		case "digitalWrite":
			if len(args) >= 2 {
				state.result.PinStates[firmwarePin(args[0])] = strings.ToLower(strings.TrimSpace(args[1]))
			}
		case "delay":
			state.result.DurationUS += int64(firmwareArgNumber(args, 0, state) * 1_000)
		case "delayMicroseconds":
			state.result.DurationUS += int64(firmwareArgNumber(args, 0, state))
		case "Wire.begin":
			// The native virtual bus is ready immediately.
		case "Wire.beginTransmission":
			state.i2cAddress = int(firmwareArgNumber(args, 0, state))
			state.i2cBytes = []int{}
		case "Wire.write":
			state.i2cBytes = append(state.i2cBytes, int(firmwareArgNumber(args, 0, state)))
		case "Wire.endTransmission":
			status := "ack"
			if !virtualI2CAddressPresent(state.result.VirtualDevices, state.i2cAddress) {
				status = "nack"
			}
			state.result.I2CTransactions = append(state.result.I2CTransactions, I2CTransaction{TimeUS: state.result.DurationUS, Address: state.i2cAddress, Bytes: append([]int(nil), state.i2cBytes...), Status: status})
			state.i2cAddress, state.i2cBytes = -1, nil
		default:
			if !strings.HasPrefix(call, "sensor.") {
				state.result.Warnings = appendUnique(state.result.Warnings, "unsupported call: "+call)
			}
		}
	}
	if state.serialLine != "" {
		state.result.SerialOutput = append(state.result.SerialOutput, state.serialLine)
		state.serialLine = ""
	}
}

func firmwareExpressionValue(expression string, state *firmwareRuntimeState) float64 {
	expression = strings.TrimSpace(expression)
	switch {
	case strings.Contains(expression, "readTemperature"):
		return firstFirmwareValue(state.variables, "temperature_c", "temperature")
	case strings.Contains(expression, "readHumidity"):
		return firstFirmwareValue(state.variables, "humidity_percent", "humidity")
	case strings.HasPrefix(expression, "analogRead("):
		return 512
	case strings.HasPrefix(expression, "digitalRead("):
		args := splitFirmwareArgs(strings.TrimSuffix(strings.TrimPrefix(expression, "digitalRead("), ")"))
		if len(args) > 0 && state.result.PinStates[firmwarePin(args[0])] == "high" {
			return 1
		}
		return 0
	}
	if value, ok := state.variables[expression]; ok {
		return value
	}
	value, _ := strconv.ParseFloat(strings.TrimSpace(expression), 64)
	return value
}

func firmwarePrintable(args []string, state *firmwareRuntimeState) string {
	if len(args) == 0 {
		return ""
	}
	value := strings.TrimSpace(args[0])
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		if decoded, err := strconv.Unquote(value); err == nil {
			return decoded
		}
	}
	if number, ok := state.variables[value]; ok {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	if strings.Contains(value, "readTemperature") {
		return strconv.FormatFloat(firstFirmwareValue(state.variables, "temperature_c", "temperature"), 'f', -1, 64)
	}
	if strings.Contains(value, "readHumidity") {
		return strconv.FormatFloat(firstFirmwareValue(state.variables, "humidity_percent", "humidity"), 'f', -1, 64)
	}
	return value
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

func firmwareArgNumber(args []string, index int, state *firmwareRuntimeState) float64 {
	if index >= len(args) {
		return 0
	}
	value := strings.TrimSpace(args[index])
	if strings.HasPrefix(strings.ToLower(value), "0x") {
		parsed, _ := strconv.ParseInt(value[2:], 16, 64)
		return float64(parsed)
	}
	return firmwareExpressionValue(value, state)
}
func firmwarePin(value string) string { return strings.Trim(strings.TrimSpace(value), `"'`) }
func firstFirmwareValue(values map[string]float64, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return 0
}
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
