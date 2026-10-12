package main

import "embed"

//go:embed native/opus/assets/darwin-arm64.gz
var rtcOpusAssets embed.FS
