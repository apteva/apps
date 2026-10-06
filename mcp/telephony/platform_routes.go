package main

// Platform-principal route management.
//
// Inbound routes were historically created only by the agent that answers
// them: the MCP tools took the owner from the caller context and refused any
// caller without an agent id. API keys and the dashboard panel carry no agent
// id, so they could configure flows and destinations but never bind a number.
//
// This file lets those platform principals manage routes on behalf of a named
// agent while agents keep exclusive ownership of their own routes. Delegated
// application users never reach these paths: operatorPhoneTool rejects them on
// MCP and applicationUserHTTP denies HTTP actions outside the softphone scopes.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// routeForCaller loads an inbound route and enforces ownership. Agent callers
// may only touch routes they own. A zero agent id identifies a platform
// principal, which is scoped to the current project only.
func (a *App) routeForCaller(ctx *sdk.AppCtx, routeID string, agentID int64) (*routeRow, error) {
	routeID = strings.TrimSpace(routeID)
	if routeID == "" {
		return nil, errors.New("route_id required")
	}
	route, err := a.db().findRoute(routeID)
	if err != nil {
		return nil, fmt.Errorf("load route: %w", err)
	}
	if route == nil {
		return nil, errors.New("unknown route_id")
	}
	if route.ProjectID != currentProject(ctx) || (agentID != 0 && route.AgentID != agentID) {
		return nil, errors.New("route belongs to another agent or project")
	}
	return route, nil
}

// agentIDArg reads an agent id from tool or JSON arguments. JSON numbers arrive
// as float64; API callers may also send the id as a string.
func agentIDArg(args map[string]any) int64 {
	switch v := args["agent_id"].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		if parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return parsed
		}
	}
	return 0
}

// resolveRouteOwner decides which agent a new inbound route belongs to.
// Agents own what they create and cannot hand routes to other agents.
// Platform principals carry no agent id and must name the owner explicitly.
func (a *App) resolveRouteOwner(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (int64, error) {
	requested := agentIDArg(args)
	if callerID := callerAgentID(callerCtx); callerID != 0 {
		if requested != 0 && requested != callerID {
			return 0, errors.New("agents can only create routes for themselves; omit agent_id")
		}
		return callerID, nil
	}
	if requested <= 0 {
		return 0, errors.New("agent_id required: the caller is not an agent, so name the agent that should own this route")
	}
	if err := a.validateProjectAgent(ctx, requested); err != nil {
		return 0, err
	}
	return requested, nil
}

// validateProjectAgent confirms an agent exists and belongs to the current
// project before a platform principal binds a route to it.
func (a *App) validateProjectAgent(ctx *sdk.AppCtx, agentID int64) error {
	agent, err := ctx.GetAgent(agentID)
	if err != nil {
		return fmt.Errorf("agent %d could not be verified: %w", agentID, err)
	}
	if agent == nil {
		return fmt.Errorf("agent %d not found", agentID)
	}
	if project := currentProject(ctx); agent.ProjectID != "" && project != "" && agent.ProjectID != project {
		return fmt.Errorf("agent %d belongs to another project", agentID)
	}
	return nil
}

// projectAgents lists the agents a panel user may bind a number to.
func (a *App) projectAgents(ctx *sdk.AppCtx) (map[string]any, error) {
	agents, err := sdk.ListAgentsVia(ctx.PlatformAPI(), currentProject(ctx))
	if err != nil {
		return nil, fmt.Errorf("list project agents: %w", err)
	}
	out := make([]map[string]any, 0, len(agents))
	for _, agent := range agents {
		out = append(out, map[string]any{
			"id":         agent.ID,
			"name":       agent.Name,
			"status":     agent.Status,
			"project_id": agent.ProjectID,
		})
	}
	return map[string]any{"agents": out}, nil
}

// createRouteFromPanel creates a route owned by an explicit agent and, unless
// asked not to, configures the carrier in the same call. An optional flow_id
// assigns a published flow so a panel user finishes in one step. Carrier or
// flow failures after the route exists are reported as a warning rather than
// an error, because the route row is real and the panel must show it.
func (a *App) createRouteFromPanel(ctx *sdk.AppCtx, body map[string]any) (map[string]any, error) {
	agentID := agentIDArg(body)
	if agentID <= 0 {
		return nil, errors.New("agent_id required")
	}
	if err := a.validateProjectAgent(ctx, agentID); err != nil {
		return nil, err
	}
	route, next, err := a.createInboundRoute(ctx, agentID, body)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"ok":                 true,
		"route":              routePublic(a, *route),
		"inbound_url":        a.inboundRouteURL(*route),
		"next":               next,
		"carrier_configured": false,
	}
	var warnings []string
	if boolArg(body, "configure", true) {
		if err := a.configureRouteCarrier(ctx, route); err != nil {
			warnings = append(warnings, "carrier configuration failed: "+err.Error())
		} else {
			result["carrier_configured"] = true
			result["route"] = routePublic(a, *route)
		}
	}
	if flowID := strings.TrimSpace(strArg(body, "flow_id", "")); flowID != "" {
		validation, assignErr := a.assignRoutingFlowToNumbers(currentProject(ctx), flowID, []string{route.ID}, nil)
		switch {
		case assignErr != nil:
			warnings = append(warnings, "flow assignment failed: "+assignErr.Error())
		case validation != nil && !validation.Valid:
			problems := append([]string{}, validation.Errors...)
			for _, number := range validation.Numbers {
				problems = append(problems, number.Errors...)
			}
			warnings = append(warnings, "flow assignment rejected: "+strings.Join(problems, "; "))
		default:
			result["flow_id"] = flowID
			if refreshed, findErr := a.db().findRoute(route.ID); findErr == nil && refreshed != nil {
				result["route"] = routePublic(a, *refreshed)
			}
		}
	}
	if len(warnings) > 0 {
		result["warning"] = strings.Join(warnings, "; ")
	}
	return result, nil
}
