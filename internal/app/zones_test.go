// Tests of the list of domain zones and its three tiers.
package app

import (
	"regexp"
	"strings"
	"testing"
)

// TestZones: zones.txt is a list of zone names without repeats, and the
// three tiers are its first 100, its first 500 and all of it.
func TestZones(t *testing.T) {
	if !strings.HasPrefix(zonesFile, "#") {
		t.Error("zones.txt does not start with the comment that says where it comes from")
	}
	name := regexp.MustCompile(`^([a-z]+|xn--[a-z0-9-]+)$`)
	seen := map[string]bool{}
	var zones []string
	for line := range strings.SplitSeq(strings.TrimSpace(zonesFile), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !name.MatchString(line) {
			t.Errorf("zones.txt has the line %q, which is no zone name in lower case", line)
		}
		if seen[line] {
			t.Errorf("zones.txt names %q twice", line)
		}
		seen[line] = true
		zones = append(zones, line)
	}
	if len(zones) <= 1000 {
		t.Fatalf("zones.txt has %d zones, want more than 1000", len(zones))
	}

	for tier, want := range map[string]int{"top100": 100, "top500": 500, "all": len(zones)} {
		set := bareZones(tier)
		if len(set) != want {
			t.Errorf("bareZones(%q) has %d zones, want %d", tier, len(set), want)
		}
		for _, zone := range zones[:want] {
			if !set[zone] {
				t.Errorf("bareZones(%q) lacks %q, one of the first %d of zones.txt", tier, zone, want)
				break
			}
		}
	}
	if !bareZones("top100")["com"] {
		t.Error(`bareZones("top100") lacks "com"`)
	}
	if got, want := bareURLZoneTiers, []string{"top100", "top500", "all"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("bareURLZoneTiers = %v, want %v", got, want)
	}
}
