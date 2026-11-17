//go:build windows

package daemon

import (
	"errors"
	"syscall"
)

// wsaAddrInUse is WSAEADDRINUSE (10048), the error a Windows listen returns when another process holds the address.
// The syscall package does not export the winsock codes, so it is spelled here; errors.Is unwraps the *net.OpError
// around it, and addrInUseText covers a wrapper that carries only the message.
const wsaAddrInUse = syscall.Errno(10048)

// isAddrInUse reports whether a listen failed because another process holds the address.
func isAddrInUse(err error) bool {
	return errors.Is(err, wsaAddrInUse) || addrInUseText(err)
}
