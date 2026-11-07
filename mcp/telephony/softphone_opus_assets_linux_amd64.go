package main

import "embed"

//go:embed native/opus/assets/linux-amd64-*.gz
var rtcOpusAssets embed.FS
