package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func mustPortfolio(t *testing.T, ctx *sdk.AppCtx, id int64) *Portfolio {
	t.Helper()
	portfolio, err := dbGetPortfolio(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	return portfolio
}
