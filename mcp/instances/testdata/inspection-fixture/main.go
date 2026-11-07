package main

import (
	"net"
	"os"
	"strconv"
)

func main() {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile(os.Args[1], []byte(strconv.Itoa(l.Addr().(*net.TCPAddr).Port)), 0600); e != nil {
		panic(e)
	}
	for {
		c, e := l.Accept()
		if e != nil {
			return
		}
		c.Close()
	}
}
