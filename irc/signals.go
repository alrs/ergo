//go:build !plan9
// +build !plan9

// Copyright (c) 2020 Shivaram Lingamneni
// released under the MIT license

package irc

import (
	"os"
	"syscall"
)

var (
	// ServerExitSignals are the signals the server will exit on.
	ServerExitSignals = []os.Signal{
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT,
	}
)
