// Domain zones: the last labels of host names, by which text mode tells a
// URL written without a scheme from an ordinary word with a dot.
package app

import (
	_ "embed"
	"strings"
)

// zonesFile is every domain zone of IANA in lower case, the most used
// first; "make zones" writes it.
//
//go:embed zones.txt
var zonesFile string

// bareURLZoneTiers are the accepted values of bare_url_zones: the 100 most
// used zones, the 500 most used, or all of them.
var bareURLZoneTiers = []string{"top100", "top500", "all"}

// zoneSets holds the set of zones of each tier, built once.
var zoneSets = func() map[string]map[string]bool {
	var zones []string
	for line := range strings.SplitSeq(zonesFile, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			zones = append(zones, line)
		}
	}
	sets := map[string]map[string]bool{}
	for tier, size := range map[string]int{"top100": 100, "top500": 500, "all": len(zones)} {
		set := make(map[string]bool, size)
		for _, zone := range zones[:min(size, len(zones))] {
			set[zone] = true
		}
		sets[tier] = set
	}
	return sets
}()

// bareZones returns the zones of a tier, one of bareURLZoneTiers. The set
// is shared: nobody changes it.
func bareZones(tier string) map[string]bool {
	set, ok := zoneSets[tier]
	if !ok {
		panic("unknown tier of zones: " + tier)
	}
	return set
}
