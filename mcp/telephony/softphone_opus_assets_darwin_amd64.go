package main

import "embed"

//go:embed native/opus/assets/darwin-amd64.gz
var rtcOpusAssets embed.FS
