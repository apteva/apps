package main

import (
	"context"
	sdk "github.com/apteva/app-sdk"
	"net"
)

// Invoked before ICE gathering, including listener/coaching and probe sockets.
func attachRTCResourcePolicy(c *softphoneRTCConfig, ctx *sdk.AppCtx) {
	if ctx != nil {
		c.socketPolicy = func(conn *net.UDPConn) sdk.SocketPolicyResult { return ctx.ApplyUDPSocketPolicy(conn, "webrtc_media") }
	}
}

// Cached playback bypasses this admission; only downloads/conversion occupy a
// slot. A queued request can cancel without deleting the durable original.
func resourceRecordingBuild(c context.Context, app *sdk.AppCtx, build func() (string, error)) func() (string, error) {
	return func() (string, error) {
		var path string
		err := app.WithWorkload(c, "recording_playback", func(context.Context) error { var e error; path, e = build(); return e })
		return path, err
	}
}
