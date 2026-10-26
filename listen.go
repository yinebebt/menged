package main

import (
	"net"
	"strings"
)

// loopbackListen reports whether addr is bound only to a loopback interface.
// Bare ":8080" or "0.0.0.0:8080" is not loopback.
func loopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
