package daemon

import "strings"

// addrInUseText is the portable fallback for isAddrInUse: the two message texts the platforms print for a held
// address, lower-cased and matched as substrings so a wrapped error still reads correctly.
func addrInUseText(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "only one usage of each socket address")
}
