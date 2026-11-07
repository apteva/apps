package main

import sdk "github.com/apteva/app-sdk"

// shut up the linter when we don't reference some imported names.
var _ sdk.Logger = (*nopLogger)(nil)

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}
