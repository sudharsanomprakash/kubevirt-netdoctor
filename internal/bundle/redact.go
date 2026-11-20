package bundle

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// Redactor replaces IP and MAC addresses with stable placeholders, so the
// same address maps to the same placeholder in every file of a bundle and
// relationships stay visible. Loopback, unspecified and documentation
// prefixes are kept because they carry meaning and identify nothing.
type Redactor struct {
	ips  map[string]string
	macs map[string]string
}

// NewRedactor returns an empty redactor.
func NewRedactor() *Redactor {
	return &Redactor{ips: map[string]string{}, macs: map[string]string{}}
}

var (
	macRE = regexp.MustCompile(`(?i)\b[0-9a-f]{2}(?::[0-9a-f]{2}){5}\b`)
	// Candidate IP tokens: IPv4 dotted quads (with optional prefix length)
	// and IPv6-looking runs containing at least two colons.
	ipRE = regexp.MustCompile(`(?i)\b(?:\d{1,3}\.){3}\d{1,3}\b|(?:[0-9a-f]{0,4}:){2,7}[0-9a-f]{0,4}(?:%[0-9a-z]+)?`)
)

var keep = []netip.Prefix{
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// Redact returns content with addresses replaced.
func (r *Redactor) Redact(content []byte) []byte {
	s := macRE.ReplaceAllStringFunc(string(content), func(m string) string {
		k := strings.ToLower(m)
		if v, ok := r.macs[k]; ok {
			return v
		}
		v := fmt.Sprintf("mac-%d", len(r.macs)+1)
		r.macs[k] = v
		return v
	})
	s = ipRE.ReplaceAllStringFunc(s, func(m string) string {
		zone := ""
		addrText := m
		if i := strings.IndexByte(m, '%'); i >= 0 {
			addrText, zone = m[:i], m[i:]
		}
		a, err := netip.ParseAddr(addrText)
		if err != nil || a.IsLoopback() || a.IsUnspecified() {
			return m
		}
		for _, p := range keep {
			if p.Contains(a) {
				return m
			}
		}
		k := a.String()
		if v, ok := r.ips[k]; ok {
			return v + zone
		}
		fam := "ip4"
		if a.Is6() && !a.Is4In6() {
			fam = "ip6"
		}
		v := fmt.Sprintf("%s-redacted-%d", fam, len(r.ips)+1)
		r.ips[k] = v
		return v + zone
	})
	return []byte(s)
}

// IPs returns how many distinct IP addresses were replaced.
func (r *Redactor) IPs() int { return len(r.ips) }

// MACs returns how many distinct MAC addresses were replaced.
func (r *Redactor) MACs() int { return len(r.macs) }
