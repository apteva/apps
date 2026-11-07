package main

import "embed"

//go:embed native/opus/assets/linux-arm64-*.gz
var rtcOpusAssets embed.FS
