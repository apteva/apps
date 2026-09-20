package main

// The GraphQL engine owns field collection, coercion, introspection, abstract
// types and result completion. Source adapters implement resolvers only.
import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	gql "github.com/graphql-go/graphql"
	gast "github.com/graphql-go/graphql/language/ast"
	"github.com/vektah/gqlparser/v2/ast"
)

type standardRequestKey struct{}
type standardRequest struct {
	project, api       string
	bindings           *executionBindings
	policy             securityPolicy
	loader             *resolverLoader
	mutation           bool
	synchronous        bool
	fastProjectionUsed bool
	timingMu           sync.Mutex
	sourceDuration     time.Duration
	errorMu            sync.Mutex
	errorCodes         map[string]string
	moduleMu           sync.Mutex
	moduleMemo         map[string]any
}

func (a *App) executeStandard(ctx context.Context, project, api, key string, schema *ast.Schema, req graphqlRequest, op *ast.OperationDefinition, doc *ast.QueryDocument, policy securityPolicy) (executeResult, error) {
	runtime, err := a.standardSchema(key, schema)
	if err != nil {
		return executeResult{}, internal(fmt.Sprintf("cannot build executable schema: %s", err))
	}
	bindings, _ := ctx.Value(executionBindingsKey{}).(*executionBindings)
	state := &standardRequest{project: project, api: api, bindings: bindings, policy: policy, mutation: op.Operation == ast.Mutation}
	ctx = context.WithValue(ctx, standardRequestKey{}, state)
	state.loader = newResolverLoader(a, ctx, project)
	result, inputErr := executeRuntime(ctx, runtime, schema, doc, op, req.Variables)
	if inputErr != nil {
		result = inputError(inputErr)
	}
	out := executeResult{OperationName: op.Name, OperationType: string(op.Operation)}
	state.timingMu.Lock()
	out.Timings.Source = state.sourceDuration
	state.timingMu.Unlock()
	out.Timings.Fast = state.fastProjectionUsed
	out.Data, _ = result.Data.(map[string]any)
	out.HasData = result.Data != nil
	for _, e := range result.Errors {
		item := map[string]any{"message": e.Message, "locations": e.Locations}
		if len(e.Path) > 0 {
			item["path"] = e.Path
			out.HasData = true
		}
		if len(e.Extensions) > 0 {
			item["extensions"] = e.Extensions
		}
		encoded, _ := json.Marshal(e.Path)
		state.errorMu.Lock()
		code := state.errorCodes[string(encoded)]
		state.errorMu.Unlock()
		if code != "" {
			item["extensions"] = map[string]any{"code": code}
		}
		out.Errors = append(out.Errors, item)
	}
	return out, nil
}

func (a *App) standardSchema(key string, schema *ast.Schema) (*gql.Schema, error) {
	a.cacheMu.RLock()
	cached := a.runtimeCache[key]
	a.cacheMu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	runtime, err := buildStandardSchema(schema, a.standardResolve)
	if err != nil {
		return nil, err
	}
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if a.runtimeCache == nil || len(a.runtimeCache) >= compiledCacheLimit {
		a.runtimeCache = map[string]*gql.Schema{}
	}
	a.runtimeCache[key] = runtime
	return runtime, nil
}

func buildStandardSchema(schema *ast.Schema, resolve gql.FieldResolveFn) (*gql.Schema, error) {
	types := map[string]gql.Type{"String": gql.String, "ID": gql.ID, "Int": gql.Int, "Float": gql.Float, "Boolean": gql.Boolean}
	var convert func(*ast.Type) gql.Type
	convert = func(t *ast.Type) gql.Type {
		var value gql.Type
		if t.Elem != nil {
			value = gql.NewList(convert(t.Elem))
		} else {
			value = types[t.NamedType]
		}
		if t.NonNull {
			return gql.NewNonNull(value)
		}
		return value
	}
	defaultValue := func(v *ast.Value) any {
		if v == nil {
			return nil
		}
		result, _ := v.Value(nil)
		return result
	}
	args := func(definitions ast.ArgumentDefinitionList) gql.FieldConfigArgument {
		out := gql.FieldConfigArgument{}
		for _, arg := range definitions {
			out[arg.Name] = &gql.ArgumentConfig{Type: convert(arg.Type), Description: arg.Description, DefaultValue: defaultValue(arg.DefaultValue)}
		}
		return out
	}
	deprecated := func(d ast.DirectiveList) string {
		if directive := d.ForName("deprecated"); directive != nil {
			if reason := directive.Arguments.ForName("reason"); reason != nil {
				value, _ := reason.Value.Value(nil)
				return fmt.Sprint(value)
			}
			return "No longer supported"
		}
		return ""
	}
	fields := func(def *ast.Definition) gql.Fields {
		out := gql.Fields{}
		for _, field := range def.Fields {
			if strings.HasPrefix(field.Name, "__") {
				continue
			}
			out[field.Name] = &gql.Field{Type: convert(field.Type), Args: args(field.Arguments), Description: field.Description, DeprecationReason: deprecated(field.Directives), Resolve: standardArguments(resolve)}
		}
		return out
	}
	resolveType := func(p gql.ResolveTypeParams) *gql.Object {
		if row, ok := p.Value.(map[string]any); ok {
			name, _ := row["__typename"].(string)
			value, _ := types[name].(*gql.Object)
			return value
		}
		return nil
	}
	for name, def := range schema.Types {
		if strings.HasPrefix(name, "__") || types[name] != nil {
			continue
		}
		switch def.Kind {
		case ast.Object:
			types[name] = gql.NewObject(gql.ObjectConfig{Name: name, Description: def.Description, Fields: gql.FieldsThunk(func() gql.Fields { return fields(def) }), Interfaces: gql.InterfacesThunk(func() []*gql.Interface {
				out := []*gql.Interface{}
				for _, name := range def.Interfaces {
					out = append(out, types[name].(*gql.Interface))
				}
				return out
			})})
		case ast.Interface:
			if len(def.Interfaces) > 0 {
				return nil, fmt.Errorf("interface inheritance is not supported by this runtime")
			}
			types[name] = gql.NewInterface(gql.InterfaceConfig{Name: name, Description: def.Description, Fields: gql.FieldsThunk(func() gql.Fields { return fields(def) }), ResolveType: resolveType})
		case ast.Union:
			types[name] = gql.NewUnion(gql.UnionConfig{Name: name, Description: def.Description, Types: gql.UnionTypesThunk(func() []*gql.Object {
				out := []*gql.Object{}
				for _, name := range def.Types {
					out = append(out, types[name].(*gql.Object))
				}
				return out
			}), ResolveType: resolveType})
		case ast.InputObject:
			if def.Directives.ForName("oneOf") != nil {
				return nil, fmt.Errorf("@oneOf inputs are not supported by this runtime")
			}
			types[name] = gql.NewInputObject(gql.InputObjectConfig{Name: name, Description: def.Description, Fields: gql.InputObjectConfigFieldMapThunk(func() gql.InputObjectConfigFieldMap {
				out := gql.InputObjectConfigFieldMap{}
				for _, f := range def.Fields {
					out[f.Name] = &gql.InputObjectFieldConfig{Type: convert(f.Type), Description: f.Description, DefaultValue: defaultValue(f.DefaultValue)}
				}
				return out
			})})
		case ast.Enum:
			values := gql.EnumValueConfigMap{}
			for _, v := range def.EnumValues {
				values[v.Name] = &gql.EnumValueConfig{Value: v.Name, Description: v.Description, DeprecationReason: deprecated(v.Directives)}
			}
			types[name] = gql.NewEnum(gql.EnumConfig{Name: name, Description: def.Description, Values: values})
		case ast.Scalar:
			// Application-defined scalars retain the existing JSON passthrough
			// contract; built-in scalars are strictly completed by the engine.
			types[name] = gql.NewScalar(gql.ScalarConfig{Name: name, Description: def.Description, Serialize: func(v any) any { return v }, ParseValue: func(v any) any { return v }, ParseLiteral: literalJSON})
		}
	}
	config := gql.SchemaConfig{}
	if schema.Query != nil {
		config.Query, _ = types[schema.Query.Name].(*gql.Object)
	}
	if schema.Mutation != nil {
		config.Mutation, _ = types[schema.Mutation.Name].(*gql.Object)
	}
	if schema.Subscription != nil {
		config.Subscription, _ = types[schema.Subscription.Name].(*gql.Object)
	}
	for _, t := range types {
		config.Types = append(config.Types, t)
	}
	config.Directives = append(config.Directives, gql.SpecifiedDirectives...)
	for name, directive := range schema.Directives {
		if name == "skip" || name == "include" || name == "deprecated" {
			continue
		}
		locations := []string{}
		for _, l := range directive.Locations {
			locations = append(locations, string(l))
		}
		config.Directives = append(config.Directives, gql.NewDirective(gql.DirectiveConfig{Name: name, Description: directive.Description, Locations: locations, Args: args(directive.Arguments)}))
	}
	result, err := gql.NewSchema(config)
	if err == nil {
		for _, t := range result.TypeMap() {
			if abstract, ok := t.(gql.Abstract); ok {
				result.IsPossibleType(abstract, config.Query)
			}
		}
	}
	return &result, err
}

func literalJSON(value gast.Value) any {
	switch v := value.(type) {
	case *gast.StringValue:
		return v.Value
	case *gast.BooleanValue:
		return v.Value
	case *gast.IntValue:
		n, _ := strconv.ParseInt(v.Value, 10, 64)
		return n
	case *gast.FloatValue:
		n, _ := strconv.ParseFloat(v.Value, 64)
		return n
	case *gast.EnumValue:
		if v.Value == "null" {
			return nil
		}
		return v.Value
	case *gast.ListValue:
		out := []any{}
		for _, x := range v.Values {
			out = append(out, literalJSON(x))
		}
		return out
	case *gast.ObjectValue:
		out := map[string]any{}
		for _, f := range v.Fields {
			out[f.Name.Value] = literalJSON(f.Value)
		}
		return out
	}
	return nil
}

type resolverError struct{ error }

func (e resolverError) Unwrap() error { return e.error }

func (e resolverError) Extensions() map[string]any { return map[string]any{"code": errorCode(e.error)} }

func (a *App) standardResolve(p gql.ResolveParams) (any, error) {
	state, _ := p.Context.Value(standardRequestKey{}).(*standardRequest)
	if state == nil {
		return gql.DefaultResolveFn(p)
	}
	fieldKey := p.Info.ParentType.Name() + "." + p.Info.FieldName
	if err := requirePermissions(securityIdentity(p.Context), state.policy.Fields[fieldKey]); err != nil {
		return nil, resolverError{err}
	}
	r, found := state.bindings.resolvers[fieldKey]
	if !found {
		return gql.DefaultResolveFn(p)
	}
	source, found := state.bindings.sources[r.SourceID]
	if !found {
		return nil, resolverError{internal("resolver source not found")}
	}
	config := mergeMaps(source.Config, r.Config)
	if source.Kind == "tables" {
		if err := validateTableRelation(r.Operation, config); err != nil {
			return nil, resolverError{err}
		}
		mandatory, err := identityWhere(p.Context, state.policy, fieldKey)
		if err != nil {
			return nil, resolverError{err}
		}
		if len(mandatory) > 0 {
			config["identity_where"] = mandatory
		}
		applyTablesEnvelopeSelection(p, r.Operation, config)
		applyTablesProjection(p, state, r.Operation, config)
	}
	config["_project_id"] = state.project
	config["graphql_args"] = p.Args
	config["parent"] = p.Source
	call := func() (any, error) {
		var value any
		var err error
		switch source.Kind {
		case "tables":
			value, err = a.callTables(p.Context, r.Operation, config)
		case "database":
			value, err = a.callDatabase(p.Context, r.Operation, config)
		case "function":
			value, err = a.callFunction(p.Context, config, p.Args)
		case "http":
			value, err = a.callHTTP(p.Context, config, p.Args)
		case "module":
			value, err = resolveModuleValue(p.Context, source.Config, r.Config, p.Source, p.Args, state.bindings)
		default:
			err = invalid("unsupported source kind %q", source.Kind)
		}
		if err != nil {
			return nil, resolverError{err}
		}
		return value, nil
	}
	if state.mutation {
		return call()
	}
	if source.Kind == "tables" {
		input, err := mappedTablesInput(config, p.Args)
		if err != nil {
			return nil, resolverError{err}
		}
		tool := "rows_" + strings.ToLower(r.Operation)
		if r.Operation == "find" || r.Operation == "list" {
			tool = "rows_search"
		}
		if tool == "rows_search" || tool == "rows_get" || tool == "rows_count" || tool == "rows_aggregate" {
			distinct, err := tablesDistinct(r.Operation, config, p.Args)
			if err != nil {
				return nil, resolverError{err}
			}
			if distinct != nil {
				input["limit"] = distinct.scanLimit
			}
			input["_project_id"] = state.project
			baseRead := state.loader.load(tool, input, r.Operation)
			read := trackResolverError(state, p, func() (any, error) {
				value, err := baseRead()
				if err != nil {
					return nil, err
				}
				return distinct.apply(value)
			})
			if state.synchronous {
				return read()
			}
			return read, nil
		}
	}
	path, _ := json.Marshal(p.Info.Path.AsArray())
	read := trackResolverError(state, p, state.loader.deferCall("field:"+string(path), call))
	if state.synchronous {
		return read()
	}
	return read, nil
}

func trackResolverError(state *standardRequest, p gql.ResolveParams, read func() (any, error)) func() (any, error) {
	return func() (any, error) {
		value, err := read()
		if err != nil {
			key, _ := json.Marshal(p.Info.Path.AsArray())
			state.errorMu.Lock()
			if state.errorCodes == nil {
				state.errorCodes = map[string]string{}
			}
			state.errorCodes[string(key)] = errorCode(err)
			state.errorMu.Unlock()
		}
		return value, err
	}
}

// Plain column mappings are source adapter configuration, not a query language.
// No client value can replace the configured table or relationship predicate.
func mappedTablesInput(config, args map[string]any) (map[string]any, error) {
	input := tablesReadInput(config, args)
	delete(input, "where")
	if table, ok := config["table"].(string); ok && table != "" {
		input["table"] = table
	}
	columns := filterColumnMap(config["filter_columns"])
	where := []any{}
	// Source/resolver and identity predicates are mandatory. Client filters
	// are appended, never allowed to replace configured scope.
	for _, raw := range []any{config["where"], config["identity_where"], args["where"]} {
		predicates, err := graphqlWhere(raw, columns)
		if err != nil {
			return nil, err
		}
		where = append(where, predicates...)
	}
	if raw, exists := config["relation"]; exists {
		relation, ok := raw.(map[string]any)
		if !ok {
			return nil, invalid("relation must be a column mapping")
		}
		local, _ := relation["parent_key"].(string)
		foreign, _ := relation["foreign_key"].(string)
		if local == "" || foreign == "" {
			return nil, invalid("relation requires parent_key and foreign_key")
		}
		parent, ok := config["parent"].(map[string]any)
		if !ok {
			return nil, invalid("relationship resolver requires a parent record")
		}
		value, exists := parent[local]
		if !exists || value == nil {
			return nil, invalid("parent relationship key %q is missing", local)
		}
		var err error
		value, err = coerceRelationValue(value, relation["value_type"])
		if err != nil {
			return nil, err
		}
		where = append(where, map[string]any{"col": foreign, "op": "eq", "value": value})
	}
	if len(where) > 0 {
		if len(where) > 100 {
			return nil, invalid("where exceeds maximum of 100 predicates")
		}
		input["where"] = where
	}
	return input, nil
}

// Tables columns are typed, while internal row IDs arrive from JSON as numbers.
// Some schemas intentionally store those IDs as text foreign keys. Explicit
// adapter coercion keeps this mapping generic and avoids guessing from values.
func coerceRelationValue(value, rawType any) (any, error) {
	kind, _ := rawType.(string)
	switch strings.TrimSpace(strings.ToLower(kind)) {
	case "":
		return value, nil
	case "string":
		return fmt.Sprint(value), nil
	case "number":
		switch n := value.(type) {
		case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
			return n, nil
		case string:
			parsed, err := strconv.ParseFloat(n, 64)
			if err != nil {
				return nil, invalid("relationship value cannot be coerced to number")
			}
			return parsed, nil
		default:
			return nil, invalid("relationship value cannot be coerced to number")
		}
	case "boolean":
		if b, ok := value.(bool); ok {
			return b, nil
		}
		if s, ok := value.(string); ok {
			parsed, err := strconv.ParseBool(s)
			if err == nil {
				return parsed, nil
			}
		}
		return nil, invalid("relationship value cannot be coerced to boolean")
	default:
		return nil, invalid("relation value_type must be string, number, or boolean")
	}
}

func validateTableRelation(operation string, config map[string]any) error {
	raw, exists := config["relation"]
	if !exists {
		return nil
	}
	if operation != "find" && operation != "list" && operation != "search" && operation != "count" && operation != "aggregate" {
		return invalid("relation requires a filtered Tables read operation")
	}
	r, ok := raw.(map[string]any)
	if !ok {
		return invalid("relation must be an object")
	}
	for _, key := range []string{"parent_key", "foreign_key"} {
		if value, ok := r[key].(string); !ok || strings.TrimSpace(value) == "" {
			return invalid("relation requires %s", key)
		}
	}
	if rawType, exists := r["value_type"]; exists {
		valueType, ok := rawType.(string)
		if !ok || (valueType != "string" && valueType != "number" && valueType != "boolean") {
			return invalid("relation value_type must be string, number, or boolean")
		}
	}
	return nil
}
