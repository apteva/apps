package main

import (
	"context"
	"fmt"
	sdk "github.com/apteva/app-sdk"
)

// Op retains the existing policy/result shape; Canonical is sent to Database.
func advancedDatabaseRead(args map[string]any) bool {
	for _, key := range []string{"pipeline", "as", "joins", "computed", "filter", "distinct", "pick", "group", "branches", "rollup", "relation", "match"} {
		if args[key] != nil {
			return true
		}
	}
	return false
}
func (step *databasePipelineStep) canonicalRead(args map[string]any) {
	if (step.Op != "find" && step.Op != "aggregate") || !advancedDatabaseRead(args) {
		return
	}
	step.Canonical = step.Op
	switch {
	case step.Op == "aggregate":
		step.Op = joinedAggregate
	case args["relation"] != nil:
		step.Op = joinedRelatedRead
	default:
		step.Op = joinedPage
	}
}
func (step databasePipelineStep) databaseOperation() string {
	if step.Canonical != "" {
		return step.Canonical
	}
	return step.Op
}

// Visit copied nodes with the shared policy binder and collection collector.
func (v joinedVisitor) pipeline(owner map[string]any, path string) error {
	return v.object(owner, "pipeline", path, func(pipeline map[string]any, path string) error {
		return v.list(pipeline, "nodes", path, func(node map[string]any, path string) error {
			rewritten, err := v.request(node, path)
			if err != nil {
				return err
			}
			for key := range node {
				delete(node, key)
			}
			for key, value := range rewritten {
				node[key] = value
			}
			return nil
		})
	})
}

func (plan *databasePipelinePlan) validateNamedRead(step *databasePipelineStep, args map[string]any, label string) error {
	if step.Canonical == "" {
		return invalid("%s: pipeline belongs to find or aggregate", label)
	}
	for _, key := range []string{"collection", "as", "joins", "computed", "filter", "distinct", "pick", "group", "branches", "rollup", "relation", "match"} {
		if args[key] != nil {
			return invalid("%s: pipeline cannot be mixed with %s", label, key)
		}
	}
	if step.Canonical == "find" && args["lookups"] != nil {
		return invalid("%s: put lookups in pipeline join nodes", label)
	}
	p, ok := args["pipeline"].(map[string]any)
	if !ok {
		return invalid("%s pipeline must be an object", label)
	}
	if err := onlyKeys(p, label+" pipeline", "version", "nodes", "from"); err != nil {
		return err
	}
	if version, ok := strictInt(p["version"]); !ok || version != 1 {
		return invalid("%s pipeline version must be 1", label)
	}
	if !literalFieldName(p["from"]) {
		return invalid("%s pipeline requires a literal from", label)
	}
	nodes, ok := p["nodes"].([]any)
	if !ok || len(nodes) == 0 || len(nodes) > 32 {
		return invalid("%s pipeline requires 1 to 32 nodes", label)
	}
	deps := map[string][]string{}
	for i, raw := range nodes {
		n, ok := raw.(map[string]any)
		path := fmt.Sprintf("%s.pipeline.nodes[%d]", label, i)
		if !ok {
			return invalid("%s must be an object", path)
		}
		if err := onlyKeys(n, path, "name", "op", "from", "collection", "where", "select", "computed", "predicate", "groupBy", "metrics", "as", "joins", "maxRows"); err != nil {
			return err
		}
		name, _ := n["name"].(string)
		if !literalFieldName(name) {
			return invalid("%s requires a literal name", path)
		}
		if _, exists := deps[name]; exists {
			return invalid("%s repeats relation %s", label, name)
		}
		deps[name] = nil
		switch n["op"] {
		case "scan":
			if !literalFieldName(n["collection"]) || n["from"] != nil {
				return invalid("%s scan requires a literal collection and no from", path)
			}
		case "compute", "aggregate", "join":
			if !literalFieldName(n["from"]) || n["collection"] != nil {
				return invalid("%s requires a literal from and no collection", path)
			}
			deps[name] = append(deps[name], n["from"].(string))
		default:
			return invalid("%s op must be scan, compute, join or aggregate", path)
		}
		for _, raw := range asAnyList(n["joins"]) {
			join, ok := raw.(map[string]any)
			if !ok {
				return invalid("%s joins must be objects", path)
			}
			if err := onlyKeys(join, path+" join", "relation", "as", "kind", "on", "cardinality", "where"); err != nil {
				return err
			}
			if !literalFieldName(join["relation"]) {
				return invalid("%s join requires a literal relation", path)
			}
			deps[name] = append(deps[name], join["relation"].(string))
		}
	}
	if _, exists := deps[p["from"].(string)]; !exists {
		return invalid("%s pipeline from names an unknown relation", label)
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(name string) error {
		if state[name] == 1 {
			return invalid("%s pipeline has a dependency cycle at %s", label, name)
		}
		if state[name] == 2 {
			return nil
		}
		links, exists := deps[name]
		if !exists {
			return invalid("%s pipeline references unknown relation %s", label, name)
		}
		state[name] = 1
		for _, dep := range links {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = 2
		return nil
	}
	for name := range deps {
		if err := visit(name); err != nil {
			return err
		}
	}
	if limits, exists := args["limits"]; exists {
		l, ok := limits.(map[string]any)
		if !ok {
			return invalid("%s limits must be an object", label)
		}
		if err := onlyKeys(l, label+" limits", "intermediateRows", "memoryBytes", "work"); err != nil {
			return err
		}
		for key, value := range l {
			if n, ok := strictInt(value); !ok || n < 0 {
				return invalid("%s limits.%s must be a nonnegative integer", label, key)
			}
		}
	}
	if step.Canonical == "aggregate" {
		metrics := asAnyList(args["metrics"])
		if len(metrics) == 0 {
			return invalid("%s aggregate requires metrics", label)
		}
		for _, raw := range metrics {
			metric, _ := raw.(map[string]any)
			name, _ := metric["name"].(string)
			if name == "" {
				return invalid("%s metric requires a name", label)
			}
			switch metric["op"] {
			case "count", "count_distinct", "sum":
				step.Metrics[name] = true
			}
		}
	}
	return nil
}

// Fixed advanced inputs on plain resolvers share the pipeline executor. Declared
// identity and access params use the existing database_pipeline contract.
func canonicalResolverPlan(operation string, config map[string]any) (map[string]any, error) {
	if operation != "find" && operation != "aggregate" {
		return nil, invalid("advanced Database reads require find or aggregate")
	}
	args := map[string]any{}
	for key := range databasePipelineStepArgs[operation] {
		if value, exists := config[key]; exists {
			args[key] = value
		}
	}
	client := resolverArgs(config)
	if untrustedFilterHasRelation(client["where"]) {
		return nil, invalid("where cannot hold exists or not_exists")
	}
	if where := client["where"]; where != nil {
		if server := args["where"]; server != nil {
			args["where"] = map[string]any{"and": []any{server, where}}
		} else {
			args["where"] = where
		}
	}
	bound := boundedArg(config["limit"], client["limit"])
	if bound == nil {
		bound = 1000
	}
	step := map[string]any{"id": "read", "op": operation, "args": args, "max_rows": bound}
	plan := map[string]any{"database": stringValue(config["database"], "default"), "scope": stringValue(config["scope"], "project"), "steps": []any{step}, "result": "$steps.read"}
	for _, key := range []string{"_project_id", "_source_id", "watch_collections"} {
		if value, exists := config[key]; exists {
			plan[key] = value
		}
	}
	if client["cursor"] != nil {
		return nil, invalid("advanced find pagination uses database_pipeline page with a declared cursor param")
	}
	return plan, nil
}

// An unconditional scan may have neither a filter nor a projection. Discover its
// primary key for an empty compiler probe; never sample business records.
func (run *databasePipelineRun) canonicalProbe(ctx context.Context, step databasePipelineStep) (map[string]any, error) {
	if step.Args["pipeline"] == nil {
		return step.joinedProbe()
	}
	args, err := (joinedVisitor{}).request(step.Args, "step "+step.ID)
	if err != nil {
		return nil, err
	}
	pipeline := args["pipeline"].(map[string]any)
	for _, raw := range asAnyList(pipeline["nodes"]) {
		node, _ := raw.(map[string]any)
		if node["op"] != "scan" || joinedRootField(node) != "" {
			continue
		}
		var schema struct {
			PrimaryKey []string `json:"primaryKey"`
		}
		input := map[string]any{"database": run.plan.Database, "scope": run.plan.Scope, "collection": node["collection"], "_project_id": run.project}
		if err := sdk.CallAppResultContext(ctx, run.app.ctx.WithProject(run.project).PlatformAPI(), "database", "db_collection_describe", input, &schema); err != nil {
			return nil, databaseCallError(err)
		}
		if len(schema.PrimaryKey) == 0 {
			return nil, invalid("Database did not describe a primary key for %v", node["collection"])
		}
		none := matchNothing(schema.PrimaryKey[0])
		if where := node["where"]; where != nil {
			node["where"] = map[string]any{"and": []any{where, none}}
		} else {
			node["where"] = none
		}
	}
	step.Args = args
	return step.joinedProbe()
}
