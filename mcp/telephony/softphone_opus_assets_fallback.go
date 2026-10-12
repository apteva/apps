//go:build !((linux || darwin) && (amd64 || arm64))

package main

import "embed"

var rtcOpusAssets embed.FS
