package main

import "syscall"

var unixSysProcAttr = syscall.SysProcAttr{Setsid: true}
