// Tests for which codes may be handed out.
package app

import "testing"

func TestAcceptable(t *testing.T) {
	d := &Domain{Host: "short.example", Static: map[string]string{"android": "https://example.com/android"}}
	for _, code := range []string{"android", "AnDrOiD", "api", "API", "Api", "healthz", "HealthZ", "shit9"} {
		if Acceptable(code, d, nil) {
			t.Errorf("Acceptable(%q) = true, want false", code)
		}
	}
	eq(t, Acceptable("Q9q8Z", d, nil), true)
	eq(t, Acceptable("Q9q8Z", d, []string{"q9q"}), false)
}
