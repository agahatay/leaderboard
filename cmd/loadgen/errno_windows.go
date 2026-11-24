package main

import "syscall"

// Winsock reports these instead of the POSIX errno values.
const (
	errConnRefused syscall.Errno = 10061 // WSAECONNREFUSED
	errConnReset   syscall.Errno = 10054 // WSAECONNRESET
)
