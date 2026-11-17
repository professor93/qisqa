// Which codes may be handed out: the store numbers the codes, and this
// decides which of them a domain may use.
package app

import (
	"slices"
	"strings"
)

// Acceptable reports whether code may be handed out on domain d: it is not
// a reserved route, not one of the domain's static aliases (aliases ignore
// letter case) and contains no blacklisted word.
func Acceptable(code string, d *Domain, extra []string) bool {
	if slices.Contains(reservedAliases, strings.ToLower(code)) {
		return false
	}
	if _, taken := d.StaticURL(code); taken {
		return false
	}
	return !Blocked(code, extra)
}
