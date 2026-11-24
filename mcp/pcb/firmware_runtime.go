package main

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The behavioral Arduino runtime interprets the subset of C++ that board
// bring-up sketches use: globals, #define constants, setup/loop and simple
// user functions, if/else, for, while, assignments (=, +=, -=, *=, /=, ++,
// --), arithmetic and logic expressions, and the Arduino calls listed in
// firmwareBuiltinCalls. It is deterministic: time only advances through
// delay/delayMicroseconds, and inputs come from FirmwareOptions.

const firmwareRuntimeName = "apteva-arduino-behavioral/0.10"

const firmwareMaxStatements = 200_000

var (
	firmwareDeclPattern   = regexp.MustCompile(`^(?:(?:const|static|volatile|unsigned|signed)\s+)*(?:float|double|int|long|short|bool|boolean|auto|byte|char|word|size_t|uint8_t|uint16_t|uint32_t|uint64_t|int8_t|int16_t|int32_t|int64_t|String)\s+([A-Za-z_]\w*)\s*(?:=\s*(.+))?$`)
	firmwareAssignPattern = regexp.MustCompile(`^([A-Za-z_]\w*)\s*(=|\+=|-=|\*=|/=|%=|\|=|&=)\s*(.+)$`)
	firmwareIncPattern    = regexp.MustCompile(`^(?:([A-Za-z_]\w*)\s*(\+\+|--)|(\+\+|--)\s*([A-Za-z_]\w*))$`)
	firmwareObjectPattern = regexp.MustCompile(`^[A-Za-z_]\w*\s+[A-Za-z_]\w*\s*(\(.*\))?$`)
	firmwareFuncPattern   = regexp.MustCompile(`(?m)^[ \t]*(?:(?:static|inline)\s+)*(?:void|int|float|double|long|bool|boolean|byte|unsigned\s+\w+|uint\w+|int\w+_t|String)\s+([A-Za-z_]\w*)\s*\(([^)]*)\)\s*\{`)
	firmwareDefinePattern = regexp.MustCompile(`^#\s*define\s+([A-Za-z_]\w*)\s+(.+)$`)
)

var firmwareConstants = map[string]float64{
	"HIGH": 1, "LOW": 0, "true": 1, "false": 0, "INPUT": 0, "OUTPUT": 1, "INPUT_PULLUP": 2,
	"LED_BUILTIN": 13, "A0": 14, "A1": 15, "A2": 16, "A3": 17, "A4": 18, "A5": 19,
	"DHT22": 22, "DHT11": 11, "PI": math.Pi,
}

type fwStmt struct {
	kind string // simple | block | if | for | while | break | continue | return
	text string
	cond string
	init string
	post string
	body []fwStmt
	alt  []fwStmt
}

type fwFunction struct {
	params []string
	body   []fwStmt
}

type fwSignal int

const (
	fwNone fwSignal = iota
	fwBreak
	fwContinue
	fwReturn
	fwAbort
)

type firmwareRuntimeState struct {
	result     *FirmwareRunResult
	options    FirmwareOptions
	variables  map[string]float64
	functions  map[string]fwFunction
	serialLine string
	i2cAddress int
	i2cBytes   []int
	returned   float64
	depth      int
}

func runFirmwareLab(def *Definition, options FirmwareOptions) (*FirmwareRunResult, error) {
	if strings.TrimSpace(options.Source) == "" {
		return nil, fmt.Errorf("firmware source required")
	}
	if len(options.Source) > maxFirmwareSourceBytes {
		return nil, fmt.Errorf("firmware source exceeds %d bytes", maxFirmwareSourceBytes)
	}
	if options.Language == "" {
		options.Language = "arduino"
	}
	if strings.ToLower(options.Language) != "arduino" && strings.ToLower(options.Language) != "cpp" {
		return nil, fmt.Errorf("the firmware lab supports Arduino-compatible C++")
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
	sensors := map[string]float64{"temperature": 23.5, "temperature_c": 23.5, "humidity": 45, "humidity_percent": 45}
	for key, value := range options.SensorValues {
		sensors[strings.ToLower(key)] = value
	}
	options.SensorValues = sensors
	result := &FirmwareRunResult{Schema: firmwareSchema, Engine: engineVersion, Status: "passed", Language: "arduino", Board: options.Board, Runtime: firmwareRuntimeName, Iterations: options.Iterations, SerialOutput: []string{}, PinModes: map[string]string{}, PinStates: map[string]string{}, I2CTransactions: []I2CTransaction{}, Variables: map[string]float64{}, Warnings: []string{}}
	_, _, _, devices := compileSimulationModel(def, nil)
	result.VirtualDevices = devices
	state := &firmwareRuntimeState{result: result, options: options, variables: map[string]float64{}, functions: map[string]fwFunction{}, i2cAddress: -1, i2cBytes: []int{}}
	for name, value := range firmwareBoardPins(def) {
		state.variables[name] = float64(value)
	}

	source := stripFirmwareComments(options.Source)
	globals, err := state.loadProgram(source)
	if err != nil {
		return nil, err
	}
	_, hasSetup := state.functions["setup"]
	_, hasLoop := state.functions["loop"]
	if !hasSetup && !hasLoop {
		return nil, fmt.Errorf("Arduino source must define setup() or loop()")
	}
	if state.exec(globals) == fwAbort {
		return finishFirmware(state), nil
	}
	if hasSetup {
		if state.call("setup", nil) == fwAbort {
			return finishFirmware(state), nil
		}
	}
	for iteration := 0; iteration < options.Iterations && hasLoop; iteration++ {
		if state.call("loop", nil) == fwAbort {
			break
		}
	}
	return finishFirmware(state), nil
}

func finishFirmware(state *firmwareRuntimeState) *FirmwareRunResult {
	result := state.result
	if state.serialLine != "" {
		result.SerialOutput = append(result.SerialOutput, state.serialLine)
		state.serialLine = ""
	}
	pins := firmwareBoardPins(nil)
	for key, value := range state.variables {
		if _, isPin := pins[key]; isPin || strings.HasPrefix(key, "PIN_") {
			continue
		}
		result.Variables[key] = roundFloat(value, 6)
	}
	return result
}

// loadProgram removes preprocessor lines and function definitions, records
// #define constants and functions, and returns the remaining top-level
// statements (global declarations) to run before setup().
func (s *firmwareRuntimeState) loadProgram(source string) ([]fwStmt, error) {
	lines := strings.Split(source, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if match := firmwareDefinePattern.FindStringSubmatch(trimmed); match != nil {
				s.variables[match[1]] = s.eval(match[2])
			}
			kept = append(kept, "")
			continue
		}
		kept = append(kept, line)
	}
	source = strings.Join(kept, "\n")
	var globals strings.Builder
	cursor := 0
	for _, loc := range firmwareFuncPattern.FindAllStringSubmatchIndex(source, -1) {
		if loc[0] < cursor {
			continue
		}
		name := source[loc[2]:loc[3]]
		params := source[loc[4]:loc[5]]
		end := matchingBrace(source, loc[1]-1)
		if end < 0 {
			return nil, fmt.Errorf("function %s() has an unterminated body", name)
		}
		body, err := parseFirmwareBlock(source[loc[1]:end])
		if err != nil {
			return nil, fmt.Errorf("%s(): %w", name, err)
		}
		s.functions[name] = fwFunction{params: firmwareParams(params), body: body}
		globals.WriteString(source[cursor:loc[0]])
		cursor = end + 1
	}
	globals.WriteString(source[cursor:])
	return parseFirmwareBlock(globals.String())
}

func firmwareParams(raw string) []string {
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		fields := strings.Fields(strings.NewReplacer("&", " ", "*", " ").Replace(part))
		if len(fields) == 0 || (len(fields) == 1 && fields[0] == "void") {
			continue
		}
		out = append(out, fields[len(fields)-1])
	}
	return out
}

// matchingBrace returns the index of the brace closing the one at open,
// ignoring braces inside string and character literals.
func matchingBrace(src string, open int) int {
	depth, quoted := 0, byte(0)
	for i := open; i < len(src); i++ {
		c := src[i]
		if quoted != 0 {
			if c == '\\' {
				i++
			} else if c == quoted {
				quoted = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quoted = c
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func matchingParen(src string, open int) int {
	depth, quoted := 0, byte(0)
	for i := open; i < len(src); i++ {
		c := src[i]
		if quoted != 0 {
			if c == '\\' {
				i++
			} else if c == quoted {
				quoted = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quoted = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func parseFirmwareBlock(src string) ([]fwStmt, error) {
	out := []fwStmt{}
	i := 0
	for {
		i = skipSpace(src, i)
		if i >= len(src) {
			return out, nil
		}
		stmt, next, err := parseFirmwareStatement(src, i)
		if err != nil {
			return nil, err
		}
		if stmt.kind != "" {
			out = append(out, stmt)
		}
		i = next
	}
}

func skipSpace(src string, i int) int {
	for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
		i++
	}
	return i
}

func keywordAt(src string, i int, word string) bool {
	if !strings.HasPrefix(src[i:], word) {
		return false
	}
	end := i + len(word)
	return end >= len(src) || !(src[end] == '_' || src[end] >= 'a' && src[end] <= 'z' || src[end] >= 'A' && src[end] <= 'Z' || src[end] >= '0' && src[end] <= '9')
}

func parseFirmwareStatement(src string, i int) (fwStmt, int, error) {
	i = skipSpace(src, i)
	if i >= len(src) {
		return fwStmt{}, i, nil
	}
	if src[i] == ';' {
		return fwStmt{}, i + 1, nil
	}
	if src[i] == '{' {
		end := matchingBrace(src, i)
		if end < 0 {
			return fwStmt{}, 0, fmt.Errorf("unterminated block")
		}
		body, err := parseFirmwareBlock(src[i+1 : end])
		return fwStmt{kind: "block", body: body}, end + 1, err
	}
	for _, kw := range []string{"if", "while", "for"} {
		if !keywordAt(src, i, kw) {
			continue
		}
		j := skipSpace(src, i+len(kw))
		if j >= len(src) || src[j] != '(' {
			return fwStmt{}, 0, fmt.Errorf("%s needs a parenthesised condition", kw)
		}
		end := matchingParen(src, j)
		if end < 0 {
			return fwStmt{}, 0, fmt.Errorf("unterminated %s condition", kw)
		}
		header := src[j+1 : end]
		body, next, err := parseFirmwareStatement(src, end+1)
		if err != nil {
			return fwStmt{}, 0, err
		}
		stmt := fwStmt{kind: kw, cond: strings.TrimSpace(header), body: []fwStmt{body}}
		if kw == "for" {
			parts := splitFirmwareTopLevel(header, ';')
			if len(parts) != 3 {
				return fwStmt{}, 0, fmt.Errorf("for needs init; condition; step")
			}
			stmt.init, stmt.cond, stmt.post = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		}
		if kw == "if" {
			k := skipSpace(src, next)
			if keywordAt(src, k, "else") {
				alt, after, err := parseFirmwareStatement(src, k+len("else"))
				if err != nil {
					return fwStmt{}, 0, err
				}
				stmt.alt = []fwStmt{alt}
				next = after
			}
		}
		return stmt, next, nil
	}
	// Simple statement: read to the terminating semicolon.
	depth, quoted := 0, byte(0)
	for j := i; j < len(src); j++ {
		c := src[j]
		if quoted != 0 {
			if c == '\\' {
				j++
			} else if c == quoted {
				quoted = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quoted = c
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ';':
			if depth == 0 {
				return simpleFirmwareStatement(src[i:j]), j + 1, nil
			}
		}
	}
	return simpleFirmwareStatement(src[i:]), len(src), nil
}

func simpleFirmwareStatement(text string) fwStmt {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return fwStmt{}
	case text == "break":
		return fwStmt{kind: "break"}
	case text == "continue":
		return fwStmt{kind: "continue"}
	case text == "return":
		return fwStmt{kind: "return"}
	case strings.HasPrefix(text, "return "), strings.HasPrefix(text, "return("):
		return fwStmt{kind: "return", text: strings.TrimSpace(strings.TrimPrefix(text, "return"))}
	}
	return fwStmt{kind: "simple", text: text}
}

func splitFirmwareTopLevel(body string, sep byte) []string {
	out, start, depth, quoted := []string{}, 0, 0, byte(0)
	for i := 0; i < len(body); i++ {
		c := body[i]
		if quoted != 0 {
			if c == '\\' {
				i++
			} else if c == quoted {
				quoted = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quoted = c
		case '(':
			depth++
		case ')':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, body[start:i])
				start = i + 1
			}
		}
	}
	return append(out, body[start:])
}

func (s *firmwareRuntimeState) budget() bool {
	s.result.Statements++
	if s.result.Statements > firmwareMaxStatements {
		s.result.Status = "failed"
		s.result.Warnings = appendUnique(s.result.Warnings, fmt.Sprintf("statement budget of %d exceeded; the sketch may loop forever without delay()", firmwareMaxStatements))
		return false
	}
	return true
}

func (s *firmwareRuntimeState) exec(stmts []fwStmt) fwSignal {
	for _, stmt := range stmts {
		if !s.budget() {
			return fwAbort
		}
		switch stmt.kind {
		case "block":
			if sig := s.exec(stmt.body); sig != fwNone {
				return sig
			}
		case "if":
			branch := stmt.alt
			if s.eval(stmt.cond) != 0 {
				branch = stmt.body
			}
			if sig := s.exec(branch); sig != fwNone {
				return sig
			}
		case "while":
			for s.eval(stmt.cond) != 0 {
				sig := s.exec(stmt.body)
				if sig == fwBreak {
					break
				}
				if sig == fwReturn || sig == fwAbort {
					return sig
				}
				if !s.budget() {
					return fwAbort
				}
			}
		case "for":
			if stmt.init != "" {
				s.simple(stmt.init)
			}
			for stmt.cond == "" || s.eval(stmt.cond) != 0 {
				sig := s.exec(stmt.body)
				if sig == fwBreak {
					break
				}
				if sig == fwReturn || sig == fwAbort {
					return sig
				}
				if stmt.post != "" {
					s.simple(stmt.post)
				}
				if !s.budget() {
					return fwAbort
				}
			}
		case "break":
			return fwBreak
		case "continue":
			return fwContinue
		case "return":
			if stmt.text != "" {
				s.returned = s.eval(stmt.text)
			}
			return fwReturn
		case "simple":
			s.simple(stmt.text)
			if s.result.Status == "failed" {
				return fwAbort
			}
		}
	}
	return fwNone
}

func (s *firmwareRuntimeState) call(name string, args []float64) fwSignal {
	fn, ok := s.functions[name]
	if !ok {
		return fwNone
	}
	if s.depth > 32 {
		s.result.Status = "failed"
		s.result.Warnings = appendUnique(s.result.Warnings, "call depth exceeds 32")
		return fwAbort
	}
	saved := map[string]*float64{}
	for i, param := range fn.params {
		if old, exists := s.variables[param]; exists {
			value := old
			saved[param] = &value
		} else {
			saved[param] = nil
		}
		if i < len(args) {
			s.variables[param] = args[i]
		} else {
			s.variables[param] = 0
		}
	}
	s.depth++
	s.returned = 0
	sig := s.exec(fn.body)
	s.depth--
	for param, old := range saved {
		if old == nil {
			delete(s.variables, param)
		} else {
			s.variables[param] = *old
		}
	}
	if sig == fwAbort {
		return fwAbort
	}
	return fwNone
}

func (s *firmwareRuntimeState) unsupported(what string) {
	s.result.Unsupported++
	s.result.Warnings = appendUnique(s.result.Warnings, what)
}

func (s *firmwareRuntimeState) simple(text string) {
	text = strings.TrimSpace(text)
	if match := firmwareIncPattern.FindStringSubmatch(text); match != nil {
		name, op := match[1], match[2]
		if name == "" {
			name, op = match[4], match[3]
		}
		if op == "++" {
			s.variables[name]++
		} else {
			s.variables[name]--
		}
		return
	}
	if match := firmwareDeclPattern.FindStringSubmatch(text); match != nil {
		value := 0.0
		if match[2] != "" {
			value = s.eval(match[2])
		}
		s.variables[match[1]] = value
		return
	}
	if match := firmwareAssignPattern.FindStringSubmatch(text); match != nil {
		name, op, value := match[1], match[2], s.eval(match[3])
		current := s.variables[name]
		switch op {
		case "=":
			current = value
		case "+=":
			current += value
		case "-=":
			current -= value
		case "*=":
			current *= value
		case "/=":
			if value != 0 {
				current /= value
			}
		case "%=":
			if int64(value) != 0 {
				current = float64(int64(current) % int64(value))
			}
		case "|=":
			current = float64(int64(current) | int64(value))
		case "&=":
			current = float64(int64(current) & int64(value))
		}
		s.variables[name] = current
		return
	}
	if name, args, ok := splitFirmwareCall(text); ok {
		s.callStatement(name, args)
		return
	}
	if firmwareObjectPattern.MatchString(text) {
		// Object declaration such as `DHT dht(PIN, DHT22)`: the virtual
		// hardware needs no constructor state.
		return
	}
	s.unsupported("unsupported statement: " + shortLabel(text, 80))
}

// splitFirmwareCall recognises `name(args)` where name may be dotted.
func splitFirmwareCall(text string) (string, []string, bool) {
	open := strings.IndexByte(text, '(')
	if open <= 0 || !strings.HasSuffix(text, ")") || matchingParen(text, open) != len(text)-1 {
		return "", nil, false
	}
	name := strings.TrimSpace(text[:open])
	for i, r := range name {
		if !(r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return "", nil, false
		}
	}
	return name, splitFirmwareArgs(text[open+1 : len(text)-1]), true
}

func (s *firmwareRuntimeState) callStatement(name string, args []string) {
	switch name {
	case "Serial.begin":
		s.result.SerialBaud = int(s.arg(args, 0))
	case "Serial.print", "Serial.println":
		s.serialLine += s.printable(args)
		if name == "Serial.println" {
			s.result.SerialOutput = append(s.result.SerialOutput, s.serialLine)
			s.serialLine = ""
		}
	case "Serial.flush":
	case "pinMode":
		if len(args) >= 2 {
			s.result.PinModes[s.pinKey(args[0])] = strings.ToUpper(strings.TrimSpace(args[1]))
		}
	case "digitalWrite":
		if len(args) >= 2 {
			state := "low"
			if s.eval(args[1]) != 0 {
				state = "high"
			}
			s.result.PinStates[s.pinKey(args[0])] = state
		}
	case "analogWrite":
		if len(args) >= 2 {
			s.result.PinStates[s.pinKey(args[0])] = "pwm:" + strconv.Itoa(int(s.eval(args[1])))
		}
	case "delay":
		s.result.DurationUS += int64(s.arg(args, 0) * 1_000)
	case "delayMicroseconds":
		s.result.DurationUS += int64(s.arg(args, 0))
	case "Wire.begin", "Wire.setClock":
	case "Wire.beginTransmission":
		s.i2cAddress = int(s.arg(args, 0))
		s.i2cBytes = []int{}
	case "Wire.write":
		s.i2cBytes = append(s.i2cBytes, int(s.arg(args, 0)))
	case "Wire.endTransmission":
		status := "ack"
		if !virtualI2CAddressPresent(s.result.VirtualDevices, s.i2cAddress) {
			status = "nack"
		}
		s.result.I2CTransactions = append(s.result.I2CTransactions, I2CTransaction{TimeUS: s.result.DurationUS, Address: s.i2cAddress, Bytes: append([]int(nil), s.i2cBytes...), Status: status})
		s.i2cAddress, s.i2cBytes = -1, nil
	default:
		if _, ok := s.functions[name]; ok {
			values := make([]float64, len(args))
			for i, arg := range args {
				values[i] = s.eval(arg)
			}
			s.call(name, values)
			return
		}
		if strings.HasSuffix(name, ".begin") {
			return // sensor and bus objects are ready immediately
		}
		// Expression statements such as `dht.readTemperature()` evaluate
		// for their side effects (counting reads).
		if _, ok := s.builtinValue(name, args); ok {
			return
		}
		s.unsupported("unsupported call: " + name)
	}
}

func (s *firmwareRuntimeState) arg(args []string, index int) float64 {
	if index >= len(args) {
		return 0
	}
	return s.eval(args[index])
}

func (s *firmwareRuntimeState) pinKey(raw string) string {
	raw = firmwarePin(raw)
	if _, err := strconv.ParseFloat(raw, 64); err == nil {
		return raw
	}
	if len(raw) >= 2 && raw[0] == 'A' {
		if _, err := strconv.Atoi(raw[1:]); err == nil {
			return raw
		}
	}
	if value, ok := s.variables[raw]; ok {
		return strconv.FormatInt(int64(value), 10)
	}
	if value, ok := firmwareConstants[raw]; ok {
		return strconv.FormatInt(int64(value), 10)
	}
	return raw
}

func (s *firmwareRuntimeState) printable(args []string) string {
	if len(args) == 0 {
		return ""
	}
	value := strings.TrimSpace(args[0])
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		if decoded, err := strconv.Unquote(value); err == nil {
			return decoded
		}
	}
	number := s.eval(value)
	if len(args) >= 2 {
		if digits := int(s.eval(args[1])); digits >= 0 && digits <= 8 && !strings.EqualFold(strings.TrimSpace(args[1]), "HEX") {
			return strconv.FormatFloat(number, 'f', digits, 64)
		}
	}
	return strconv.FormatFloat(number, 'f', -1, 64)
}

// builtinValue evaluates Arduino calls that return a value.
func (s *firmwareRuntimeState) builtinValue(name string, args []string) (float64, bool) {
	lower := strings.ToLower(name)
	switch {
	case name == "digitalRead":
		s.result.InputReads++
		pin := s.pinKey(s.argText(args, 0))
		if value, ok := s.options.DigitalInputs[pin]; ok {
			if value != 0 {
				return 1, true
			}
			return 0, true
		}
		if s.result.PinStates[pin] == "high" || s.result.PinModes[pin] == "INPUT_PULLUP" {
			return 1, true
		}
		return 0, true
	case name == "analogRead":
		s.result.InputReads++
		pin := s.pinKey(s.argText(args, 0))
		if value, ok := s.options.AnalogInputs[pin]; ok {
			return value, true
		}
		return 512, true
	case name == "millis":
		return float64(s.result.DurationUS / 1_000), true
	case name == "micros":
		return float64(s.result.DurationUS), true
	case strings.HasSuffix(lower, "readtemperature") || strings.HasSuffix(lower, "gettemperature"):
		s.result.SensorReads++
		return firstFirmwareValue(s.options.SensorValues, "temperature_c", "temperature"), true
	case strings.HasSuffix(lower, "readhumidity") || strings.HasSuffix(lower, "gethumidity"):
		s.result.SensorReads++
		return firstFirmwareValue(s.options.SensorValues, "humidity_percent", "humidity"), true
	case name == "Wire.read":
		return 0, true
	case name == "Wire.available":
		return 0, true
	case name == "Wire.requestFrom":
		return s.arg(args, 1), true
	case name == "Serial.available":
		return 0, true
	case name == "map" && len(args) == 5:
		x, inLo, inHi, outLo, outHi := s.eval(args[0]), s.eval(args[1]), s.eval(args[2]), s.eval(args[3]), s.eval(args[4])
		if inHi == inLo {
			return outLo, true
		}
		return math.Trunc((x-inLo)*(outHi-outLo)/(inHi-inLo) + outLo), true
	case name == "constrain" && len(args) == 3:
		return math.Max(s.eval(args[1]), math.Min(s.eval(args[2]), s.eval(args[0]))), true
	case name == "min" && len(args) == 2:
		return math.Min(s.eval(args[0]), s.eval(args[1])), true
	case name == "max" && len(args) == 2:
		return math.Max(s.eval(args[0]), s.eval(args[1])), true
	case name == "abs" || name == "fabs":
		return math.Abs(s.arg(args, 0)), true
	case name == "sqrt":
		return math.Sqrt(s.arg(args, 0)), true
	case name == "pow" && len(args) == 2:
		return math.Pow(s.eval(args[0]), s.eval(args[1])), true
	case name == "round":
		return math.Round(s.arg(args, 0)), true
	case name == "floor":
		return math.Floor(s.arg(args, 0)), true
	case name == "ceil":
		return math.Ceil(s.arg(args, 0)), true
	case name == "isnan":
		return boolFloat(math.IsNaN(s.arg(args, 0))), true
	}
	if fn, ok := s.functions[name]; ok {
		_ = fn
		values := make([]float64, len(args))
		for i, arg := range args {
			values[i] = s.eval(arg)
		}
		s.call(name, values)
		return s.returned, true
	}
	if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' && !strings.Contains(name, ".") {
		return 0, true // constructor such as Adafruit_SHTC3()
	}
	return 0, false
}

func (s *firmwareRuntimeState) argText(args []string, index int) string {
	if index >= len(args) {
		return ""
	}
	return args[index]
}

func firstFirmwareValue(values map[string]float64, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return 0
}

func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// ---- expressions ----

type fwToken struct {
	kind string // num | ident | str | op
	text string
	num  float64
}

func tokenizeFirmware(expr string) ([]fwToken, error) {
	tokens := []fwToken{}
	for i := 0; i < len(expr); {
		c := expr[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(expr) && expr[i+1] >= '0' && expr[i+1] <= '9':
			j := i
			if c == '0' && i+1 < len(expr) && (expr[i+1] == 'x' || expr[i+1] == 'X') {
				j = i + 2
				for j < len(expr) && strings.ContainsRune("0123456789abcdefABCDEF", rune(expr[j])) {
					j++
				}
				value, _ := strconv.ParseInt(expr[i+2:j], 16, 64)
				tokens = append(tokens, fwToken{kind: "num", num: float64(value)})
			} else {
				for j < len(expr) && (expr[j] >= '0' && expr[j] <= '9' || expr[j] == '.' || expr[j] == 'e' || expr[j] == 'E' || (expr[j] == '-' || expr[j] == '+') && (expr[j-1] == 'e' || expr[j-1] == 'E')) {
					j++
				}
				value, err := strconv.ParseFloat(expr[i:j], 64)
				if err != nil {
					return nil, fmt.Errorf("bad number %q", expr[i:j])
				}
				tokens = append(tokens, fwToken{kind: "num", num: value})
			}
			for j < len(expr) && strings.ContainsRune("fFuUlL", rune(expr[j])) {
				j++
			}
			i = j
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i
			for j < len(expr) && (expr[j] == '_' || expr[j] == '.' || expr[j] >= 'a' && expr[j] <= 'z' || expr[j] >= 'A' && expr[j] <= 'Z' || expr[j] >= '0' && expr[j] <= '9') {
				j++
			}
			tokens = append(tokens, fwToken{kind: "ident", text: expr[i:j]})
			i = j
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(expr) && expr[j] != c {
				if expr[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(expr) {
				return nil, fmt.Errorf("unterminated literal")
			}
			literal := expr[i : j+1]
			token := fwToken{kind: "str", text: literal}
			if c == '\'' {
				if decoded, err := strconv.Unquote(literal); err == nil && len(decoded) > 0 {
					token = fwToken{kind: "num", num: float64(decoded[0])}
				}
			}
			tokens = append(tokens, token)
			i = j + 1
		default:
			two := ""
			if i+1 < len(expr) {
				two = expr[i : i+2]
			}
			switch two {
			case "||", "&&", "==", "!=", "<=", ">=", "<<", ">>":
				tokens = append(tokens, fwToken{kind: "op", text: two})
				i += 2
				continue
			}
			if strings.ContainsRune("+-*/%!<>()&|^?:,~", rune(c)) {
				tokens = append(tokens, fwToken{kind: "op", text: string(c)})
				i++
				continue
			}
			return nil, fmt.Errorf("unexpected %q", string(c))
		}
	}
	return tokens, nil
}

type fwParser struct {
	tokens []fwToken
	pos    int
	state  *firmwareRuntimeState
	err    error
}

func (s *firmwareRuntimeState) eval(expr string) float64 {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0
	}
	tokens, err := tokenizeFirmware(expr)
	if err != nil {
		s.unsupported("unsupported expression: " + shortLabel(expr, 80))
		return 0
	}
	p := &fwParser{tokens: tokens, state: s}
	value := p.ternary()
	if p.err == nil && p.pos < len(p.tokens) {
		p.err = fmt.Errorf("trailing tokens")
	}
	if p.err != nil {
		s.unsupported("unsupported expression: " + shortLabel(expr, 80))
		return 0
	}
	return value
}

func (p *fwParser) peek() *fwToken {
	if p.pos < len(p.tokens) {
		return &p.tokens[p.pos]
	}
	return nil
}

func (p *fwParser) acceptOp(ops ...string) string {
	t := p.peek()
	if t == nil || t.kind != "op" {
		return ""
	}
	for _, op := range ops {
		if t.text == op {
			p.pos++
			return op
		}
	}
	return ""
}

func (p *fwParser) ternary() float64 {
	cond := p.binary(0)
	if p.acceptOp("?") != "" {
		a := p.ternary()
		if p.acceptOp(":") == "" {
			p.err = fmt.Errorf("expected :")
			return 0
		}
		b := p.ternary()
		if cond != 0 {
			return a
		}
		return b
	}
	return cond
}

var fwBinaryLevels = [][]string{{"||"}, {"&&"}, {"|"}, {"^"}, {"&"}, {"==", "!="}, {"<", "<=", ">", ">="}, {"<<", ">>"}, {"+", "-"}, {"*", "/", "%"}}

func (p *fwParser) binary(level int) float64 {
	if level >= len(fwBinaryLevels) {
		return p.unary()
	}
	left := p.binary(level + 1)
	for {
		op := p.acceptOp(fwBinaryLevels[level]...)
		if op == "" {
			return left
		}
		right := p.binary(level + 1)
		switch op {
		case "||":
			left = boolFloat(left != 0 || right != 0)
		case "&&":
			left = boolFloat(left != 0 && right != 0)
		case "|":
			left = float64(int64(left) | int64(right))
		case "^":
			left = float64(int64(left) ^ int64(right))
		case "&":
			left = float64(int64(left) & int64(right))
		case "==":
			left = boolFloat(math.Abs(left-right) < 1e-12)
		case "!=":
			left = boolFloat(math.Abs(left-right) >= 1e-12)
		case "<":
			left = boolFloat(left < right)
		case "<=":
			left = boolFloat(left <= right)
		case ">":
			left = boolFloat(left > right)
		case ">=":
			left = boolFloat(left >= right)
		case "<<":
			left = float64(int64(left) << uint(right))
		case ">>":
			left = float64(int64(left) >> uint(right))
		case "+":
			left += right
		case "-":
			left -= right
		case "*":
			left *= right
		case "/":
			if right == 0 {
				p.state.result.Warnings = appendUnique(p.state.result.Warnings, "division by zero evaluated as 0")
				left = 0
			} else {
				left /= right
			}
		case "%":
			if int64(right) == 0 {
				left = 0
			} else {
				left = float64(int64(left) % int64(right))
			}
		}
	}
}

func (p *fwParser) unary() float64 {
	switch p.acceptOp("-", "+", "!", "~") {
	case "-":
		return -p.unary()
	case "+":
		return p.unary()
	case "!":
		return boolFloat(p.unary() == 0)
	case "~":
		return float64(^int64(p.unary()))
	}
	return p.primary()
}

func (p *fwParser) primary() float64 {
	t := p.peek()
	if t == nil {
		p.err = fmt.Errorf("unexpected end")
		return 0
	}
	switch t.kind {
	case "num":
		p.pos++
		return t.num
	case "str":
		p.pos++
		return 0
	case "op":
		if t.text == "(" {
			p.pos++
			// C-style casts: (int)x, (float)(y)
			if next := p.peek(); next != nil && next.kind == "ident" && isFirmwareType(next.text) && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].text == ")" {
				p.pos += 2
				value := p.unary()
				if next.text != "float" && next.text != "double" {
					value = math.Trunc(value)
				}
				return value
			}
			value := p.ternary()
			if p.acceptOp(")") == "" {
				p.err = fmt.Errorf("expected )")
			}
			return value
		}
	case "ident":
		p.pos++
		name := t.text
		if p.acceptOp("(") != "" {
			start := p.pos
			depth := 1
			for p.pos < len(p.tokens) && depth > 0 {
				switch p.tokens[p.pos].text {
				case "(":
					depth++
				case ")":
					depth--
				}
				p.pos++
			}
			if depth != 0 {
				p.err = fmt.Errorf("unterminated call")
				return 0
			}
			args := splitFirmwareArgs(joinFirmwareTokens(p.tokens[start : p.pos-1]))
			if isFirmwareType(name) && len(args) == 1 {
				value := p.state.eval(args[0])
				if name != "float" && name != "double" {
					value = math.Trunc(value)
				}
				return value
			}
			value, ok := p.state.builtinValue(name, args)
			if !ok {
				p.state.unsupported("unsupported call: " + name)
			}
			return value
		}
		if value, ok := p.state.variables[name]; ok {
			return value
		}
		if value, ok := firmwareConstants[name]; ok {
			return value
		}
		p.state.result.Warnings = appendUnique(p.state.result.Warnings, "undefined identifier "+name+" evaluated as 0")
		return 0
	}
	p.err = fmt.Errorf("unexpected token %q", t.text)
	return 0
}

func isFirmwareType(name string) bool {
	switch name {
	case "int", "long", "float", "double", "byte", "char", "bool", "uint8_t", "uint16_t", "uint32_t", "int16_t", "int32_t", "unsigned":
		return true
	}
	return false
}

func joinFirmwareTokens(tokens []fwToken) string {
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		switch t.kind {
		case "num":
			parts[i] = strconv.FormatFloat(t.num, 'g', -1, 64)
		default:
			parts[i] = t.text
		}
	}
	return strings.Join(parts, " ")
}

// ---- board pin header ----

var firmwareGPIOPattern = regexp.MustCompile(`^(?:GPIO|IO|D|P|PA|PB|PC|PD)?(\d{1,3})$`)

// firmwareBoardPins maps net names to the MCU GPIO numbers they connect to,
// as PIN_<NET> constants. A nil definition returns an empty map.
func firmwareBoardPins(def *Definition) map[string]int {
	out := map[string]int{}
	if def == nil {
		return out
	}
	netByNode := map[string]Net{}
	for _, net := range def.Nets {
		for _, node := range net.Nodes {
			netByNode[node.ComponentID+":"+node.PinID] = net
		}
	}
	for _, component := range def.Components {
		if !isMCUComponent(component) {
			continue
		}
		for _, pin := range component.Pins {
			gpio := -1
			for _, candidate := range []string{pin.Name, pin.Number, pin.ID} {
				if match := firmwareGPIOPattern.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(candidate))); match != nil {
					gpio, _ = strconv.Atoi(match[1])
					break
				}
			}
			net, ok := netByNode[component.ID+":"+pin.ID]
			if gpio < 0 || !ok || isFirmwarePowerNet(net) {
				continue
			}
			name := "PIN_" + firmwareMacroName(firstNonEmpty(net.Name, net.ID))
			if _, exists := out[name]; !exists {
				out[name] = gpio
			}
		}
	}
	return out
}

func isMCUComponent(c Component) bool {
	if c.Model != nil && strings.EqualFold(c.Model.Kind, "mcu") {
		return true
	}
	text := strings.ToLower(c.Name + " " + c.Value + " " + c.MPN)
	for _, hint := range []string{"esp32", "atmega", "rp2040", "stm32", "attiny", "samd", "nrf52", "microcontroller", "arduino"} {
		if strings.Contains(text, hint) {
			return true
		}
	}
	return false
}

func isFirmwarePowerNet(net Net) bool {
	upper := strings.ToUpper(net.ID + " " + net.Name)
	for _, hint := range []string{"GND", "VCC", "VDD", "3V3", "5V", "VBUS", "VIN", "VBAT"} {
		if strings.Contains(upper, hint) {
			return true
		}
	}
	return false
}

func firmwareMacroName(v string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(v) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// renderFirmwareHeader writes board_pins.h for the design's MCU.
func renderFirmwareHeader(def *Definition) []byte {
	pins := firmwareBoardPins(def)
	names := make([]string, 0, len(pins))
	for name := range pins {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("// Generated by PCB Studio from the board net list. Do not edit.\n")
	fmt.Fprintf(&b, "// Design: %s, board: %s\n", def.Name, inferFirmwareBoard(def))
	b.WriteString("#pragma once\n\n")
	for _, name := range names {
		fmt.Fprintf(&b, "#define %s %d\n", name, pins[name])
	}
	if len(names) == 0 {
		b.WriteString("// No MCU GPIO pins with signal nets were found.\n")
	}
	return []byte(b.String())
}

// firmwareGate runs the revision's stored firmware for the readiness gate.
func firmwareGate(def *Definition) (*FirmwareRunResult, []Check) {
	spec := def.Firmware
	if spec == nil && def.Wiring != nil && def.Wiring.Firmware != nil && strings.TrimSpace(def.Wiring.Firmware.Source) != "" {
		spec = &FirmwareSpec{Board: def.Wiring.Firmware.Board, Source: def.Wiring.Firmware.Source}
	}
	if spec == nil || strings.TrimSpace(spec.Source) == "" {
		return nil, []Check{{Code: "FIRMWARE_MISSING", Severity: "warning", Message: "No firmware is stored on the revision; set it with firmware.set to gate on a simulated run"}}
	}
	result, err := runFirmwareLab(def, FirmwareOptions{Source: spec.Source, Board: spec.Board, Iterations: spec.Iterations, SensorValues: spec.SensorValues, DigitalInputs: spec.DigitalInputs, AnalogInputs: spec.AnalogInputs})
	if err != nil {
		return nil, []Check{{Code: "FIRMWARE_SIMULATION", Severity: "error", Message: "Firmware did not run: " + err.Error()}}
	}
	checks := []Check{}
	if result.Status != "passed" {
		checks = append(checks, Check{Code: "FIRMWARE_SIMULATION", Severity: "error", Message: "Firmware run failed: " + strings.Join(result.Warnings, "; ")})
	}
	if result.Unsupported > 0 {
		checks = append(checks, Check{Code: "FIRMWARE_UNSUPPORTED", Severity: "error", Message: fmt.Sprintf("%d statements or calls are outside the behavioral runtime, so the run does not prove the firmware", result.Unsupported)})
	}
	output := strings.Join(result.SerialOutput, "\n")
	for _, want := range spec.ExpectSerial {
		if !strings.Contains(output, want) {
			checks = append(checks, Check{Code: "FIRMWARE_EXPECTATION", Severity: "error", Message: fmt.Sprintf("Serial output lacks %q", want)})
		}
	}
	for _, tx := range result.I2CTransactions {
		if tx.Status == "nack" {
			checks = append(checks, Check{Code: "FIRMWARE_I2C_NACK", Severity: "error", Message: fmt.Sprintf("No virtual device acknowledged I2C address 0x%02X", tx.Address)})
		}
	}
	return result, checks
}
