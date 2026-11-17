//go:build !windows

package daemon

import (
	"errors"
	"syscall"
)

// isAddrInUse reports whether a listen failed because another process holds the address: EADDRINUSE, compared with
// errors.Is so a wrapped *net.OpError counts. Windows has its own file, and addrInUseText is the fallback both share.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || addrInUseText(err)
}
