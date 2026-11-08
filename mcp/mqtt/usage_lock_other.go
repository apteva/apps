//go:build !linux && !darwin && !windows && !freebsd && !openbsd && !netbsd && !dragonfly

package main

import (
	"errors"
	"os"
)

func lockBrokerFile(*os.File) error {
	return errors.New("durable MQTT broker requires a supported local file locking platform")
}
