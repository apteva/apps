package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

type resolverModule struct {
	ID               int64
	ProjectID        string
	Name             string
	Version          int
	Status           string
	Description      string
	Inputs           map[string]any
	OutputType       string
	Definition       map[string]any
	Dependencies     []string
	Deterministic    bool
	CreatedAt        string
	PublishedAt      string
	NullBehavior     string
	DecimalPrecision int
	DecimalScale     int
	RoundingMode     string
	Timezone         string
	Completeness     string
}

func moduleKey(name string, version int) string {
	return strings.TrimSpace(name) + "@" + strconv.Itoa(version)
}

func validModuleName(name string) bool {
	parts := strings.Split(name, ".")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !graphqlName(part) {
			return false
		}
	}
	return true
}

func validModuleType(value string) bool {
	switch value {
	case "String", "ID", "Int", "Float", "Boolean", "JSON", "Decimal", "Date", "DateTime", "Duration":
		return true
	default:
		return false
	}
}

func createResolverModule(db *sql.DB, project, name, description, outputType string, inputs, definition map[string]any, version int, deterministic bool) (*resolverModule, error) {
	name = strings.TrimSpace(name)
	if !validModuleName(name) {
		return nil, invalid("module name must be dot-separated GraphQL names")
	}
	outputType = strings.TrimSpace(outputType)
	if !validModuleType(outputType) {
		return nil, invalid("unsupported resolver module output_type")
	}
	if inputs == nil {
		inputs = map[string]any{}
	}
	if definition == nil {
		return nil, invalid("definition is required")
	}
	if version <= 0 {
		if err := db.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM graphql_resolver_modules WHERE project_id=? AND name=?`, project, name).Scan(&version); err != nil {
			return nil, err
		}
	}
	existing, err := getResolverModule(db, project, name, version, false)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.Status == "published" {
		return nil, invalid("published module versions are immutable; create a new version")
	}
	if problems := validateResolverModuleDefinition(inputs, definition); len(problems) > 0 {
		return nil, invalid("invalid resolver module: %s", strings.Join(problems, "; "))
	}
	dependencies := moduleCallDependencies(definition)
	inputsJSON, _ := encodeJSON(inputs)
	definitionJSON, _ := encodeJSON(definition)
	dependenciesJSON, _ := encodeJSON(dependencies)
	now := nowUTC()
	_, err = db.Exec(`INSERT INTO graphql_resolver_modules
        (project_id,name,version,status,description,inputs_json,output_type,definition_json,dependencies_json,deterministic,created_at)
        VALUES(?,?,?,?,?,?,?,?,?,?,?)
        ON CONFLICT(project_id,name,version) DO UPDATE SET
          description=excluded.description, inputs_json=excluded.inputs_json,
          output_type=excluded.output_type, definition_json=excluded.definition_json,
          dependencies_json=excluded.dependencies_json, deterministic=excluded.deterministic`,
		project, name, version, "draft", strings.TrimSpace(description), inputsJSON, outputType, definitionJSON, dependenciesJSON, boolInt(deterministic), now)
	if err != nil {
		return nil, err
	}
	return getResolverModule(db, project, name, version, false)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func scanResolverModule(scanner interface{ Scan(...any) error }) (*resolverModule, error) {
	var row resolverModule
	var inputs, definition, dependencies string
	var deterministic int
	err := scanner.Scan(&row.ID, &row.ProjectID, &row.Name, &row.Version, &row.Status, &row.Description, &inputs, &row.OutputType, &definition, &dependencies, &deterministic, &row.CreatedAt, &row.PublishedAt, &row.NullBehavior, &row.DecimalPrecision, &row.DecimalScale, &row.RoundingMode, &row.Timezone, &row.Completeness)
	if err != nil {
		return nil, err
	}
	row.Inputs = decodeJSON(inputs)
	row.Definition = decodeJSON(definition)
	_ = json.Unmarshal([]byte(dependencies), &row.Dependencies)
	row.Deterministic = deterministic != 0
	return &row, nil
}

const moduleColumns = `id,project_id,name,version,status,description,inputs_json,output_type,definition_json,dependencies_json,deterministic,created_at,COALESCE(published_at,''),null_behavior,decimal_precision,decimal_scale,rounding_mode,timezone,completeness`

func getResolverModule(db *sql.DB, project, name string, version int, publishedOnly bool) (*resolverModule, error) {
	query := `SELECT ` + moduleColumns + ` FROM graphql_resolver_modules WHERE project_id=? AND name=?`
	args := []any{project, strings.TrimSpace(name)}
	if version > 0 {
		query += ` AND version=?`
		args = append(args, version)
	}
	if publishedOnly {
		query += ` AND status='published'`
	}
	query += ` ORDER BY version DESC LIMIT 1`
	row, err := scanResolverModule(db.QueryRow(query, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return row, err
}

func listResolverModules(db *sql.DB, project string) ([]resolverModule, error) {
	rows, err := db.Query(`SELECT `+moduleColumns+` FROM graphql_resolver_modules WHERE project_id=? ORDER BY name,version DESC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []resolverModule{}
	for rows.Next() {
		row, err := scanResolverModule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *row)
	}
	return out, rows.Err()
}

func publishResolverModule(db *sql.DB, project, name string, version int) (*resolverModule, error) {
	row, err := getResolverModule(db, project, name, version, false)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, invalid("resolver module not found")
	}
	modules, err := listResolverModules(db, project)
	if err != nil {
		return nil, err
	}
	index := map[string]resolverModule{}
	for _, module := range modules {
		index[moduleKey(module.Name, module.Version)] = module
	}
	if problems := validateModuleGraph(*row, index); len(problems) > 0 {
		return nil, invalid("cannot publish resolver module: %s", strings.Join(problems, "; "))
	}
	if row.Status != "published" {
		if _, err := db.Exec(`UPDATE graphql_resolver_modules SET status='published',published_at=? WHERE id=?`, nowUTC(), row.ID); err != nil {
			return nil, err
		}
	}
	return getResolverModule(db, project, name, version, false)
}

func publicResolverModule(row resolverModule) map[string]any {
	return map[string]any{"id": row.ID, "project_id": publicProjectID(row.ProjectID), "name": row.Name, "version": row.Version, "status": row.Status, "description": row.Description, "inputs": row.Inputs, "output_type": row.OutputType, "definition": row.Definition, "dependencies": row.Dependencies, "deterministic": row.Deterministic, "null_behavior": row.NullBehavior, "decimal_precision": row.DecimalPrecision, "decimal_scale": row.DecimalScale, "rounding_mode": row.RoundingMode, "timezone": row.Timezone, "completeness": row.Completeness, "created_at": row.CreatedAt, "published_at": row.PublishedAt}
}

type moduleMetadata struct {
	NullBehavior     string
	DecimalPrecision int
	DecimalScale     int
	RoundingMode     string
	Timezone         string
	Completeness     string
}

func parseModuleMetadata(args map[string]any) (moduleMetadata, error) {
	m := moduleMetadata{NullBehavior: stringArg(args, "null_behavior", "propagate"), DecimalPrecision: intArg(args, "decimal_precision", 34), DecimalScale: intArg(args, "decimal_scale", 12), RoundingMode: stringArg(args, "rounding_mode", "half_even"), Timezone: stringArg(args, "timezone", "UTC"), Completeness: stringArg(args, "completeness", "complete")}
	if m.NullBehavior != "propagate" && m.NullBehavior != "strict" {
		return m, invalid("null_behavior must be propagate or strict")
	}
	if m.DecimalPrecision < 1 || m.DecimalPrecision > 100 || m.DecimalScale < 0 || m.DecimalScale > m.DecimalPrecision {
		return m, invalid("invalid decimal precision or scale")
	}
	if m.RoundingMode != "half_even" && m.RoundingMode != "half_up" && m.RoundingMode != "down" {
		return m, invalid("unsupported rounding_mode")
	}
	if _, err := time.LoadLocation(m.Timezone); err != nil {
		return m, invalid("invalid timezone")
	}
	if m.Completeness != "complete" && m.Completeness != "partial" {
		return m, invalid("completeness must be complete or partial")
	}
	return m, nil
}

func setResolverModuleMetadata(db *sql.DB, id int64, m moduleMetadata) error {
	_, err := db.Exec(`UPDATE graphql_resolver_modules SET null_behavior=?,decimal_precision=?,decimal_scale=?,rounding_mode=?,timezone=?,completeness=? WHERE id=? AND status='draft'`, m.NullBehavior, m.DecimalPrecision, m.DecimalScale, m.RoundingMode, m.Timezone, m.Completeness, id)
	return err
}

func validateResolverModuleDefinition(inputs, definition map[string]any) []string {
	problems := []string{}
	for name, raw := range inputs {
		if !graphqlName(name) {
			problems = append(problems, "invalid input name "+name)
			continue
		}
		typeName, _ := raw.(string)
		if object, ok := raw.(map[string]any); ok {
			typeName, _ = object["type"].(string)
		}
		if !validModuleType(strings.TrimSuffix(typeName, "!")) {
			problems = append(problems, "invalid type for input "+name)
		}
	}
	seen := 0
	var walk func(any, string)
	walk = func(raw any, path string) {
		seen++
		if seen > 2048 {
			problems = append(problems, "definition exceeds 2048 nodes")
			return
		}
		node, ok := raw.(map[string]any)
		if !ok {
			problems = append(problems, path+" must be an expression object")
			return
		}
		if input, ok := node["input"].(string); ok {
			if _, exists := inputs[input]; !exists {
				problems = append(problems, path+" references unknown input "+input)
			}
			return
		}
		if _, exists := node["const"]; exists {
			return
		}
		op, _ := node["op"].(string)
		supported := map[string]bool{"input": true, "call": true, "add": true, "subtract": true, "multiply": true, "divide": true, "round": true, "eq": true, "neq": true, "lt": true, "lte": true, "gt": true, "gte": true, "and": true, "or": true, "not": true, "if": true, "coalesce": true, "concat": true, "sum": true, "min": true, "max": true, "length": true, "lower": true, "upper": true, "trim": true, "date_add": true, "datetime_add": true, "date_diff_days": true, "format_datetime": true}
		if !supported[op] {
			problems = append(problems, path+" has unsupported op "+op)
			return
		}
		if op == "input" {
			name, _ := node["name"].(string)
			if _, exists := inputs[name]; !exists {
				problems = append(problems, path+" references unknown input "+name)
			}
			return
		}
		if op == "call" {
			if name, _ := node["module"].(string); !validModuleName(name) {
				problems = append(problems, path+" call requires a valid module")
			}
			if moduleInt(node["version"]) <= 0 {
				problems = append(problems, path+" call requires a pinned positive version")
			}
			mapped, _ := node["inputs"].(map[string]any)
			for name, value := range mapped {
				walk(value, path+".inputs."+name)
			}
			return
		}
		if args, ok := node["args"].([]any); ok {
			for i, arg := range args {
				walk(arg, fmt.Sprintf("%s.args[%d]", path, i))
			}
		}
		for _, key := range []string{"value", "condition", "then", "else"} {
			if value, exists := node[key]; exists {
				walk(value, path+"."+key)
			}
		}
	}
	walk(definition, "definition")
	sort.Strings(problems)
	return uniqueStrings(problems)
}

func uniqueStrings(values []string) []string {
	out := []string{}
	last := ""
	for _, value := range values {
		if len(out) == 0 || value != last {
			out = append(out, value)
			last = value
		}
	}
	return out
}

func moduleInt(value any) int {
	switch value := value.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		n, _ := value.Int64()
		return int(n)
	}
	return 0
}

func moduleCallDependencies(definition map[string]any) []string {
	set := map[string]bool{}
	var walk func(any)
	walk = func(raw any) {
		switch value := raw.(type) {
		case map[string]any:
			if value["op"] == "call" {
				if name, ok := value["module"].(string); ok {
					set[moduleKey(name, moduleInt(value["version"]))] = true
				}
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(definition)
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func validateModuleGraph(root resolverModule, modules map[string]resolverModule) []string {
	problems := validateResolverModuleDefinition(root.Inputs, root.Definition)
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(resolverModule)
	visit = func(module resolverModule) {
		key := moduleKey(module.Name, module.Version)
		if visiting[key] {
			problems = append(problems, "dependency cycle at "+key)
			return
		}
		if visited[key] {
			return
		}
		visiting[key] = true
		for _, dependency := range module.Dependencies {
			called, ok := modules[dependency]
			if !ok || called.Status != "published" {
				problems = append(problems, "dependency "+dependency+" is not published")
				continue
			}
			visit(called)
		}
		delete(visiting, key)
		visited[key] = true
	}
	visit(root)
	sort.Strings(problems)
	return uniqueStrings(problems)
}

type moduleRuntime struct{ modules map[string]resolverModule }

func (runtime moduleRuntime) evaluate(module resolverModule, inputs map[string]any) (any, error) {
	if module.NullBehavior == "strict" {
		for name, definition := range module.Inputs {
			typeName, _ := definition.(string)
			if object, ok := definition.(map[string]any); ok {
				typeName, _ = object["type"].(string)
			}
			if strings.HasSuffix(typeName, "!") && inputs[name] == nil {
				return nil, invalid("required module input %s is null", name)
			}
		}
	}
	value, err := runtime.eval(module.Definition, inputs, 0)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	if module.OutputType == "Decimal" {
		n, err := moduleNumber(value)
		if err != nil {
			return nil, err
		}
		return formatDecimal(n, module.DecimalPrecision, module.DecimalScale, module.RoundingMode)
	}
	if module.OutputType == "Date" || module.OutputType == "DateTime" {
		return normalizeModuleTime(value, module.OutputType, module.Timezone)
	}
	return value, nil
}

func (runtime moduleRuntime) eval(raw any, inputs map[string]any, depth int) (any, error) {
	if depth > 64 {
		return nil, invalid("resolver module evaluation exceeds maximum depth")
	}
	node, ok := raw.(map[string]any)
	if !ok {
		return nil, invalid("invalid resolver module expression")
	}
	if value, exists := node["const"]; exists {
		return value, nil
	}
	if input, ok := node["input"].(string); ok {
		return inputs[input], nil
	}
	op, _ := node["op"].(string)
	if op == "input" {
		name, _ := node["name"].(string)
		return inputs[name], nil
	}
	if op == "call" {
		name, _ := node["module"].(string)
		version := moduleInt(node["version"])
		called, ok := runtime.modules[moduleKey(name, version)]
		if !ok || called.Status != "published" {
			return nil, invalid("resolver module dependency %s@%d is unavailable", name, version)
		}
		mapped := map[string]any{}
		rawInputs, _ := node["inputs"].(map[string]any)
		for name, expression := range rawInputs {
			value, err := runtime.eval(expression, inputs, depth+1)
			if err != nil {
				return nil, err
			}
			mapped[name] = value
		}
		return runtime.eval(called.Definition, mapped, depth+1)
	}
	values := []any{}
	if args, ok := node["args"].([]any); ok {
		for _, arg := range args {
			value, err := runtime.eval(arg, inputs, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
	}
	switch op {
	case "concat":
		var b strings.Builder
		for _, value := range values {
			if value != nil {
				b.WriteString(fmt.Sprint(value))
			}
		}
		return b.String(), nil
	case "coalesce":
		for _, value := range values {
			if value != nil && value != "" {
				return value, nil
			}
		}
		return nil, nil
	case "lower", "upper", "trim":
		if len(values) != 1 {
			return nil, invalid("%s requires one argument", op)
		}
		text := fmt.Sprint(values[0])
		if op == "lower" {
			return strings.ToLower(text), nil
		}
		if op == "upper" {
			return strings.ToUpper(text), nil
		}
		return strings.TrimSpace(text), nil
	case "length":
		if len(values) != 1 {
			return nil, invalid("length requires one argument")
		}
		switch value := values[0].(type) {
		case string:
			return int64(len([]rune(value))), nil
		case []any:
			return int64(len(value)), nil
		case map[string]any:
			return int64(len(value)), nil
		}
		return nil, invalid("length requires a string, list, or object")
	case "not":
		if len(values) != 1 {
			return nil, invalid("not requires one argument")
		}
		return !truthy(values[0]), nil
	case "and":
		for _, value := range values {
			if !truthy(value) {
				return false, nil
			}
		}
		return true, nil
	case "or":
		for _, value := range values {
			if truthy(value) {
				return true, nil
			}
		}
		return false, nil
	case "if":
		condition, err := runtime.eval(node["condition"], inputs, depth+1)
		if err != nil {
			return nil, err
		}
		branch := "else"
		if truthy(condition) {
			branch = "then"
		}
		return runtime.eval(node[branch], inputs, depth+1)
	case "eq", "neq", "lt", "lte", "gt", "gte":
		if len(values) != 2 {
			return nil, invalid("%s requires two arguments", op)
		}
		comparison, err := compareModuleValues(values[0], values[1])
		if err != nil {
			return nil, err
		}
		switch op {
		case "eq":
			return comparison == 0, nil
		case "neq":
			return comparison != 0, nil
		case "lt":
			return comparison < 0, nil
		case "lte":
			return comparison <= 0, nil
		case "gt":
			return comparison > 0, nil
		default:
			return comparison >= 0, nil
		}
	case "add", "subtract", "multiply", "divide", "sum", "min", "max":
		return calculateModuleNumber(op, values)
	case "round":
		value, err := runtime.eval(node["value"], inputs, depth+1)
		if err != nil {
			return nil, err
		}
		number, err := moduleNumber(value)
		if err != nil {
			return nil, err
		}
		places := moduleInt(node["places"])
		if places < 0 || places > 12 {
			return nil, invalid("round places must be between 0 and 12")
		}
		f, _ := number.Float64()
		scale := math.Pow10(places)
		return math.Round(f*scale) / scale, nil
	case "date_add", "datetime_add":
		if len(values) != 2 {
			return nil, invalid("%s requires a time and duration", op)
		}
		t, err := parseModuleTime(values[0], "UTC")
		if err != nil {
			return nil, err
		}
		d, err := parseModuleDuration(values[1])
		if err != nil {
			return nil, err
		}
		value := t.Add(d)
		if op == "date_add" {
			return value.Format("2006-01-02"), nil
		}
		return value.Format(time.RFC3339Nano), nil
	case "date_diff_days":
		if len(values) != 2 {
			return nil, invalid("date_diff_days requires two times")
		}
		left, err := parseModuleTime(values[0], "UTC")
		if err != nil {
			return nil, err
		}
		right, err := parseModuleTime(values[1], "UTC")
		if err != nil {
			return nil, err
		}
		return int64(left.Sub(right) / (24 * time.Hour)), nil
	case "format_datetime":
		if len(values) < 1 || len(values) > 2 {
			return nil, invalid("format_datetime requires a time and optional layout")
		}
		t, err := parseModuleTime(values[0], "UTC")
		if err != nil {
			return nil, err
		}
		layout := time.RFC3339
		if len(values) == 2 {
			layout = fmt.Sprint(values[1])
		}
		return t.Format(layout), nil
	default:
		return nil, invalid("unsupported resolver module operation %q", op)
	}
}

func formatDecimal(value *big.Rat, precision, scale int, mode string) (string, error) {
	if precision <= 0 {
		precision = 34
	}
	if scale < 0 {
		scale = 0
	}
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	scaledNum := new(big.Int).Mul(value.Num(), factor)
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(scaledNum, value.Denom(), rem)
	absRem := new(big.Int).Abs(rem)
	absDen := new(big.Int).Abs(value.Denom())
	increment := false
	switch mode {
	case "half_up":
		increment = new(big.Int).Lsh(absRem, 1).Cmp(absDen) >= 0
	case "half_even":
		cmp := new(big.Int).Lsh(absRem, 1).Cmp(absDen)
		increment = cmp > 0 || (cmp == 0 && new(big.Int).Abs(q).Bit(0) == 1)
	case "down":
	default:
		return "", invalid("unsupported rounding mode")
	}
	if increment {
		if value.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	digits := new(big.Int).Abs(q).String()
	if len(digits) > precision {
		return "", invalid("decimal exceeds precision %d", precision)
	}
	for len(digits) <= scale {
		digits = "0" + digits
	}
	if scale > 0 {
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if q.Sign() < 0 {
		digits = "-" + digits
	}
	return digits, nil
}

func parseModuleTime(value any, timezone string) (time.Time, error) {
	text, ok := value.(string)
	if !ok {
		return time.Time{}, invalid("time value must be a string")
	}
	if t, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return t, nil
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, invalid("invalid timezone")
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, text, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, invalid("invalid date/time %q", text)
}

func parseModuleDuration(value any) (time.Duration, error) {
	if text, ok := value.(string); ok {
		d, err := time.ParseDuration(text)
		if err == nil {
			return d, nil
		}
	}
	if number, err := moduleNumber(value); err == nil {
		f, _ := number.Float64()
		return time.Duration(f * float64(time.Second)), nil
	}
	return 0, invalid("invalid duration")
}

func normalizeModuleTime(value any, outputType, timezone string) (string, error) {
	t, err := parseModuleTime(value, timezone)
	if err != nil {
		return "", err
	}
	loc, _ := time.LoadLocation(timezone)
	t = t.In(loc)
	if outputType == "Date" {
		return t.Format("2006-01-02"), nil
	}
	return t.Format(time.RFC3339Nano), nil
}

func truthy(value any) bool {
	switch value := value.(type) {
	case bool:
		return value
	case nil:
		return false
	case string:
		return value != ""
	case float64:
		return value != 0
	case int:
		return value != 0
	case int64:
		return value != 0
	}
	return true
}

func moduleNumber(value any) (*big.Rat, error) {
	text := ""
	switch value := value.(type) {
	case json.Number:
		text = value.String()
	case string:
		text = value
	case float64:
		text = strconv.FormatFloat(value, 'g', -1, 64)
	case float32:
		text = strconv.FormatFloat(float64(value), 'g', -1, 32)
	case int:
		text = strconv.Itoa(value)
	case int64:
		text = strconv.FormatInt(value, 10)
	case int32:
		text = strconv.FormatInt(int64(value), 10)
	default:
		return nil, invalid("numeric operation received %T", value)
	}
	number, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, invalid("invalid decimal number %q", text)
	}
	return number, nil
}

func calculateModuleNumber(op string, values []any) (any, error) {
	if len(values) == 0 {
		return nil, invalid("%s requires arguments", op)
	}
	result, err := moduleNumber(values[0])
	if err != nil {
		return nil, err
	}
	for _, raw := range values[1:] {
		value, err := moduleNumber(raw)
		if err != nil {
			return nil, err
		}
		switch op {
		case "add", "sum":
			result.Add(result, value)
		case "subtract":
			result.Sub(result, value)
		case "multiply":
			result.Mul(result, value)
		case "divide":
			if value.Sign() == 0 {
				return nil, invalid("division by zero")
			}
			result.Quo(result, value)
		case "min":
			if result.Cmp(value) > 0 {
				result.Set(value)
			}
		case "max":
			if result.Cmp(value) < 0 {
				result.Set(value)
			}
		}
	}
	if result.IsInt() {
		if result.Num().IsInt64() {
			return result.Num().Int64(), nil
		}
	}
	value, _ := result.Float64()
	return value, nil
}

func compareModuleValues(left, right any) (int, error) {
	// Null sorts below every concrete value and equals only null. This makes
	// conventional guards such as `total > 0` false for absent data instead of
	// accidentally comparing the strings "<nil>" and "0".
	if left == nil {
		if right == nil {
			return 0, nil
		}
		return -1, nil
	}
	if right == nil {
		return 1, nil
	}
	if l, err := moduleNumber(left); err == nil {
		r, err := moduleNumber(right)
		if err == nil {
			return l.Cmp(r), nil
		}
	}
	return strings.Compare(fmt.Sprint(left), fmt.Sprint(right)), nil
}

func moduleParentDependencies(module resolverModule, mapping map[string]any, modules map[string]resolverModule) []string {
	needed := map[string]bool{}
	for input := range usedModuleInputs(module.Definition, modules, map[string]bool{}) {
		if path, ok := mapping[input].(string); ok && strings.HasPrefix(path, "$parent.") {
			column := strings.TrimPrefix(path, "$parent.")
			if graphqlName(column) {
				needed[column] = true
			}
		}
	}
	out := make([]string, 0, len(needed))
	for column := range needed {
		out = append(out, column)
	}
	sort.Strings(out)
	return out
}

func moduleInputMapping(config map[string]any) (map[string]any, error) {
	raw, exists := config["inputs"]
	if !exists {
		return map[string]any{}, nil
	}
	mapping, ok := raw.(map[string]any)
	if !ok {
		return nil, invalid("module inputs must be an object")
	}
	for name, value := range mapping {
		if !graphqlName(name) {
			return nil, invalid("invalid module input name %q", name)
		}
		if path, ok := value.(string); ok && strings.HasPrefix(path, "$") {
			valid := strings.HasPrefix(path, "$parent.") || strings.HasPrefix(path, "$args.") || path == "$identity.subject" || path == "$identity.tenant" || strings.HasPrefix(path, "$identity.claim.")
			if !valid {
				return nil, invalid("unsupported module input path %q", path)
			}
		}
	}
	return mapping, nil
}

func configuredResolverModule(sourceConfig, resolverConfig map[string]any, bindings *executionBindings) (resolverModule, map[string]any, bool) {
	if bindings == nil {
		return resolverModule{}, nil, false
	}
	config := mergeMaps(sourceConfig, resolverConfig)
	name, _ := config["module"].(string)
	version := moduleInt(config["version"])
	module, ok := bindings.modules[moduleKey(name, version)]
	if !ok || module.Status != "published" {
		return resolverModule{}, nil, false
	}
	mapping, err := moduleInputMapping(config)
	if err != nil {
		return resolverModule{}, nil, false
	}
	return module, mapping, true
}

func bindingsFromContext(ctx interface{ Value(any) any }) *executionBindings {
	bindings, _ := ctx.Value(executionBindingsKey{}).(*executionBindings)
	return bindings
}

func resolveModuleValue(ctx interface{ Value(any) any }, sourceConfig, resolverConfig map[string]any, parent any, args map[string]any, bindings *executionBindings) (any, error) {
	module, mapping, ok := configuredResolverModule(sourceConfig, resolverConfig, bindings)
	if !ok {
		return nil, invalid("published resolver module is unavailable")
	}
	inputs := map[string]any{}
	identity, _ := ctx.Value(identityKey{}).(*requestIdentity)
	for name := range module.Inputs {
		raw, exists := mapping[name]
		if !exists {
			inputs[name] = nil
			continue
		}
		inputs[name] = resolveModuleInput(raw, parent, args, identity)
	}
	encoded, _ := json.Marshal([]any{moduleKey(module.Name, module.Version), inputs})
	if state, _ := ctx.Value(standardRequestKey{}).(*standardRequest); state != nil && module.Deterministic {
		key := string(encoded)
		state.moduleMu.Lock()
		value, found := state.moduleMemo[key]
		state.moduleMu.Unlock()
		if found {
			return value, nil
		}
		value, err := (moduleRuntime{modules: bindings.modules}).evaluate(module, inputs)
		if err != nil {
			return nil, err
		}
		state.moduleMu.Lock()
		if state.moduleMemo == nil {
			state.moduleMemo = map[string]any{}
		}
		state.moduleMemo[key] = value
		state.moduleMu.Unlock()
		return value, nil
	}
	return (moduleRuntime{modules: bindings.modules}).evaluate(module, inputs)
}

func usedModuleInputs(definition map[string]any, modules map[string]resolverModule, visiting map[string]bool) map[string]bool {
	out := map[string]bool{}
	var walk func(any)
	walk = func(raw any) {
		switch value := raw.(type) {
		case map[string]any:
			if input, ok := value["input"].(string); ok {
				out[input] = true
				return
			}
			if value["op"] == "input" {
				if name, ok := value["name"].(string); ok {
					out[name] = true
				}
				return
			}
			if value["op"] == "call" {
				mapped, _ := value["inputs"].(map[string]any)
				for _, expression := range mapped {
					walk(expression)
				}
				return
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(definition)
	return out
}

func resolveModuleInput(value any, parent any, args map[string]any, identity *requestIdentity) any {
	path, ok := value.(string)
	if !ok || !strings.HasPrefix(path, "$") {
		return value
	}
	read := func(root any, parts []string) any {
		current := root
		for _, part := range parts {
			object, ok := current.(map[string]any)
			if !ok {
				return nil
			}
			current = object[part]
		}
		return current
	}
	switch {
	case strings.HasPrefix(path, "$parent."):
		return read(parent, strings.Split(strings.TrimPrefix(path, "$parent."), "."))
	case strings.HasPrefix(path, "$args."):
		return read(args, strings.Split(strings.TrimPrefix(path, "$args."), "."))
	case path == "$identity.subject":
		if identity != nil {
			return identity.Subject
		}
	case path == "$identity.tenant":
		if identity != nil {
			return identity.Tenant
		}
	case strings.HasPrefix(path, "$identity.claim."):
		if identity != nil {
			return read(identity.Claims, strings.Split(strings.TrimPrefix(path, "$identity.claim."), "."))
		}
	}
	return nil
}
