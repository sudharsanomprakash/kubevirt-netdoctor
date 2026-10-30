package inspect

import (
	"net/netip"
	"regexp"
)

var ipv4Pattern = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)

var syntheticPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/32"),  // unspecified (default route prefix)
	netip.MustParsePrefix("127.0.0.0/8"), // loopback
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
}

func syntheticIPv4(s string) bool {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return true // not an address (for example a version string)
	}
	for _, p := range syntheticPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
