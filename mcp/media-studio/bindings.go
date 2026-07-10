package main

import sdk "github.com/apteva/app-sdk"

func boundIntegrationsFor(ctx *sdk.AppCtx, role string) []*sdk.BoundIntegration {
	if ctx == nil {
		return nil
	}
	bounds := ctx.IntegrationsFor(role)
	for i, bound := range bounds {
		if bound != nil && bound.IsDefault && i > 0 {
			ordered := make([]*sdk.BoundIntegration, 0, len(bounds))
			ordered = append(ordered, bound)
			ordered = append(ordered, bounds[:i]...)
			ordered = append(ordered, bounds[i+1:]...)
			return ordered
		}
	}
	return bounds
}
