// Target URL validation: which long URLs may be shortened.
package app

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// maxURLLength is the longest target URL accepted, in bytes.
const maxURLLength = 2048

// CheckTarget decides whether raw may become a link on domain d, and
// returns the target to store: raw itself, or raw with its host in ASCII
// form when the host has letters outside ASCII (see asciiHost). The
// returned error is written for the API caller.
//
// A target must be an absolute http or https URL of at most 2048 bytes. It
// must not point at one of our own short domains (that would chain or loop
// redirects), nor at a host the operator blocked. If the domain lists
// allowed targets, the URL's host must match one of them, so a leaked token
// cannot be used to shorten arbitrary sites.
//
// Those checks compare the host with names as written. A browser reads
// other spellings as the same host: Unicode letters, dots and invisible
// characters through IDNA, numbers in other forms as an IPv4 address, and
// an IPv6 address that maps an IPv4 one as that IPv4 host. So a host with
// letters outside ASCII is first turned into the ASCII name a browser looks
// up, and every check is made on that. The other spellings are refused
// rather than guessed at, and so is a host name with an empty label, such
// as "evil.example..", which nothing can look up, or with an xn-- label
// that is not the ASCII form of a name, such as "xn--evil-.example".
func CheckTarget(raw string, d *Domain, meta *Meta) (target string, err error) {
	u, err := parseTarget(raw)
	if err != nil {
		return "", err
	}
	// Hostname() is the real host: user info ("user@") and port are excluded.
	written := u.Hostname()
	// Before lower-casing: the Kelvin sign lower-cases to an ASCII "k".
	if !isASCII(written) {
		if raw, err = asciiHost(raw, u); err != nil {
			return "", err
		}
		if u, err = parseTarget(raw); err != nil {
			return "", err
		}
	}
	host := strings.ToLower(u.Hostname())
	// An empty label hides a name from the block list, which looks at the
	// host label by label, and so does an xn-- label that a reader may
	// decode to ASCII. An address in brackets is not a name.
	if !strings.HasPrefix(u.Host, "[") && (hasEmptyLabel(host) || hasFalseXNLabel(host)) {
		return "", fmt.Errorf("url: %q is not a valid host name", written)
	}
	// An IPv6 address that maps an IPv4 one reaches that IPv4 host.
	if addr, err := netip.ParseAddr(host); err == nil && addr.Is4In6() {
		return "", fmt.Errorf("url: write the IPv4 address %s itself, not its IPv6 form", addr.Unmap())
	}
	if readAsIPv4(host) {
		// ParseAddr takes an IPv4 address only as four decimal numbers
		// without leading zeros.
		if addr, err := netip.ParseAddr(host); err != nil || !addr.Is4() {
			return "", errors.New("url: write the address as four numbers, like 192.0.2.1")
		}
	}
	if meta.Domain(u.Host) != nil {
		return "", errors.New("url is already a short link on one of this service's domains")
	}
	if meta.Blocked(host) {
		return "", fmt.Errorf("host %q is blocked", host)
	}
	if len(d.AllowedTargets) > 0 && !hostAllowed(host, d.AllowedTargets) {
		return "", fmt.Errorf("host %q is not allowed on %s", host, d.Host)
	}
	return raw, nil
}

// parseTarget parses raw as an absolute http or https URL with a host, of
// at most maxURLLength bytes.
func parseTarget(raw string) (*url.URL, error) {
	if len(raw) > maxURLLength {
		return nil, fmt.Errorf("url is longer than %d bytes", maxURLLength)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("url must start with http:// or https://")
	}
	if u.Hostname() == "" {
		return nil, errors.New("url has no host")
	}
	return u, nil
}

// asciiHost returns raw, which url.Parse turned into u, with its host, one
// with letters outside ASCII, in the ASCII form a browser looks it up by:
// that of idna.Lookup, so "пример.example" becomes "xn--e1afmkfd.example"
// and full-width letters become ASCII ones. The rest of raw is kept byte
// for byte. A host idna refuses is refused by name, and so is one written
// with % escapes, whose bytes as written are not its name.
func asciiHost(raw string, u *url.URL) (string, error) {
	name := u.Hostname()
	ascii, err := idna.Lookup.ToASCII(name)
	if err != nil {
		return "", fmt.Errorf("url: %q is not a valid host name", name)
	}
	// Where url.Parse found the host: raw starts with the scheme and "://",
	// the authority ends at the first "/", "?" or "#", and the host follows
	// its last "@", if any, up to the port.
	at := len(u.Scheme) + len("://")
	authority := raw[at:]
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	userinfo := strings.LastIndexByte(authority, '@') + 1
	if !strings.HasPrefix(authority[userinfo:], name) {
		return "", errors.New("url: write the host without % escapes")
	}
	at += userinfo
	return raw[:at] + ascii + raw[at+len(name):], nil
}

// hasEmptyLabel reports whether a host name has an empty label once one
// trailing dot is taken off: nothing is left, or it starts or ends with a
// dot, or has two in a row. No such name can be looked up.
func hasEmptyLabel(host string) bool {
	return slices.Contains(strings.Split(strings.TrimSuffix(host, "."), "."), "")
}

// hasFalseXNLabel reports whether a label of host, in lower case, that
// starts with "xn--" is not the ASCII form of a name: idna.Lookup refuses
// it or makes another of it, as it makes "evil" of "xn--evil-" and nothing
// of "xn--".
func hasFalseXNLabel(host string) bool {
	for label := range strings.SplitSeq(host, ".") {
		if strings.HasPrefix(label, "xn--") {
			if ascii, err := idna.Lookup.ToASCII(label); err != nil || ascii != label {
				return true
			}
		}
	}
	return false
}

// isASCII reports whether s holds no byte of 0x80 or above.
func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// readAsIPv4 reports whether a browser reads host, in lower case, as an
// IPv4 address: its last label, after one trailing dot, is all digits or
// starts with 0x.
func readAsIPv4(host string) bool {
	label := strings.TrimSuffix(host, ".")
	label = label[strings.LastIndexByte(label, '.')+1:]
	return strings.HasPrefix(label, "0x") || (label != "" && strings.Trim(label, "0123456789") == "")
}

// targetHost returns the host of a URL in lower case, without user info or
// port, as CheckTarget sees it.
func targetHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// hostAllowed reports whether host matches a pattern: "example.com" matches
// exactly that host, "*.example.com" matches every subdomain of it.
func hostAllowed(host string, patterns []string) bool {
	for _, pattern := range patterns {
		if suffix, wildcard := strings.CutPrefix(pattern, "*"); wildcard {
			if strings.HasSuffix(host, suffix) {
				return true
			}
		} else if host == pattern {
			return true
		}
	}
	return false
}
