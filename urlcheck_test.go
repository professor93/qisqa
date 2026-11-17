// Tests for target URL validation.
package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// targetErr returns the error of CheckTarget alone.
func targetErr(raw string, d *Domain, meta *Meta) error {
	_, err := CheckTarget(raw, d, meta)
	return err
}

// targetMeta has one unrestricted domain and one with allowed targets.
func targetMeta(t *testing.T) *Meta {
	t.Helper()
	m := newTestMeta(t)
	withDomains(t, m, "short.example")
	_, err := m.AddDomain("go.example", "", []string{"example.com", "*.example.com", "files.example", "*.files.example"}, false)
	must(t, err)
	return m
}

func TestCheckTargetOnOpenDomain(t *testing.T) {
	m := targetMeta(t)
	d := m.Domain("short.example")
	exactly2048 := "https://example.org/" + strings.Repeat("a", 2048-len("https://example.org/"))

	for _, ok := range []string{"https://example.org/a", "http://example.org", exactly2048} {
		if err := targetErr(ok, d, m); err != nil {
			t.Errorf("CheckTarget(%.40q) = %v, want nil", ok, err)
		}
	}

	rejected := map[string]string{
		"ftp://example.org/a":         "http",
		"example.org/a":               "http",
		"https://":                    "host",
		exactly2048 + "a":             "2048",
		"https://short.example/x":     "short link",
		"https://www.short.example/x": "short link",
		"https://GO.EXAMPLE:443/x":    "short link",
	}
	for raw, want := range rejected {
		t.Run(raw[:min(len(raw), 40)], func(t *testing.T) {
			wantErr(t, targetErr(raw, d, m), want)
		})
	}
}

func TestCheckTargetAllowedTargets(t *testing.T) {
	m := targetMeta(t)
	d := m.Domain("go.example")

	for _, ok := range []string{"https://example.com/a", "https://docs.example.com/a", "https://EXAMPLE.com/a", "https://cdn.files.example/a"} {
		if err := targetErr(ok, d, m); err != nil {
			t.Errorf("CheckTarget(%q) = %v, want nil", ok, err)
		}
	}
	for _, raw := range []string{
		"https://example.org/a",
		"https://badfiles.example/a",
		"https://example.com.evil.example/a",
		"https://example.com@evil.example/a",
	} {
		t.Run(raw, func(t *testing.T) {
			wantErr(t, targetErr(raw, d, m), "not allowed")
		})
	}
}

func TestCheckTargetBlockedHost(t *testing.T) {
	m := targetMeta(t)
	must(t, m.Block("example.org"))
	d := m.Domain("short.example")

	for _, raw := range []string{"https://example.org/a", "https://cdn.EXAMPLE.org:8443/a", "https://user@example.org/a"} {
		wantErr(t, targetErr(raw, d, m), "is blocked")
	}
	must(t, targetErr("https://example.com/a", d, m))
}

// lookAlikeHosts spell example.com, or 192.0.2.1, in ways a browser reads
// as that host. A host with letters outside ASCII is taken in its ASCII
// form, the name a browser looks up, which is then checked as any other:
// target is what the URL becomes. Any other spelling is refused, and
// refused holds the part of the error that says why.
var lookAlikeHosts = map[string]struct{ target, refused string }{
	"https://ｅｘａｍｐｌｅ.com/x":         {target: "https://example.com/x"},             // full-width letters
	"https://%EF%BD%85xample.com/x": {refused: "write the host without % escapes"}, // a full-width letter, percent-encoded
	"https://example。com/x":         {target: "https://example.com/x"},             // an ideographic full stop
	"https://exam­ple.com/x":        {target: "https://example.com/x"},             // a soft hyphen, which a browser drops
	"https://3221225985/":           {refused: "write the address as four numbers, like 192.0.2.1"},
	"https://[::ffff:c000:201]/":    {refused: "write the IPv4 address 192.0.2.1 itself"}, // 192.0.2.1 mapped into IPv6
	"https://[::ffff:192.0.2.1]/":   {refused: "write the IPv4 address 192.0.2.1 itself"},
}

func TestCheckTargetHostSpellings(t *testing.T) {
	m := targetMeta(t)
	d := m.Domain("short.example")
	for _, ok := range []string{
		"https://xn--e1afmkfd.example/", // an international name in its xn-- form
		"https://example.com/",
		"https://192.0.2.1/",
		"https://example.com:8443/x",
		"https://[2001:db8::1]/",
	} {
		if err := targetErr(ok, d, m); err != nil {
			t.Errorf("CheckTarget(%q) = %v, want nil", ok, err)
		}
	}
	target, err := CheckTarget("https://Kiwi.example/", d, m) // the Kelvin sign is a K to a browser
	must(t, err)
	eq(t, target, "https://kiwi.example/")

	refused := map[string]string{
		"https://0xc0.0.2.1/":  "four numbers",
		"https://0XC0000201/":  "four numbers",
		"https://192.0.2.01/":  "four numbers",
		"https://192.0.2.0x1/": "four numbers",
		"https://1.2.3/":       "four numbers",
		"https://192.0.2.1./":  "four numbers",
	}
	for raw, want := range refused {
		wantErr(t, targetErr(raw, d, m), want)
	}
	for raw, want := range lookAlikeHosts {
		target, err := CheckTarget(raw, d, m)
		if want.refused != "" {
			wantErr(t, err, want.refused)
			continue
		}
		must(t, err)
		eq(t, target, want.target)
	}
	// With the hosts blocked a spelling still meets its refusal, and a host
	// taken in its ASCII form meets the block.
	must(t, m.Block("example.com"))
	must(t, m.Block("192.0.2.1"))
	for raw, want := range refused {
		wantErr(t, targetErr(raw, d, m), want)
	}
	for raw, want := range lookAlikeHosts {
		if want.refused != "" {
			wantErr(t, targetErr(raw, d, m), want.refused)
		} else {
			wantErr(t, targetErr(raw, d, m), `host "example.com" is blocked`)
		}
	}
}

// A host with letters outside ASCII is taken in its ASCII form, the name a
// browser looks up: that is what is checked, stored and returned. Nothing
// else of the URL changes, and a URL whose host is ASCII stays as it is,
// byte for byte. A host that has no ASCII form is refused by name.
func TestTargetHostBecomesASCII(t *testing.T) {
	m := targetMeta(t)
	d := m.Domain("short.example")
	for raw, want := range map[string]string{
		"https://пример.example/a?b#c":                        "https://xn--e1afmkfd.example/a?b#c",
		"https://user:pw@ПРИМЕР.example:8443/путь?я=%D1%8F#ф": "https://user:pw@xn--e1afmkfd.example:8443/путь?я=%D1%8F#ф",
		"HTTP://ｅｘａｍｐｌｅ.org":                                  "HTTP://example.org",
		"https://пример.example?q":                            "https://xn--e1afmkfd.example?q",
		"https://пример.example#a/b":                          "https://xn--e1afmkfd.example#a/b",
	} {
		target, err := CheckTarget(raw, d, m)
		must(t, err)
		eq(t, target, want)
	}
	for _, ascii := range []string{
		"https://User:Pw@Example.COM:8443/a%2Fb/../c?d=%20e&f#G",
		"HTTP://EXAMPLE.ORG",
		"https://xn--e1afmkfd.example/путь",
		"https://[2001:DB8::1]:8443/",
		"https://example.org/%EF%BD%85",
	} {
		target, err := CheckTarget(ascii, d, m)
		must(t, err)
		eq(t, target, ascii)
	}
	for raw, host := range map[string]string{
		"https://-пример.example/":       "-пример.example",       // a hyphen first
		"https://ex\u200dample.example/": "ex\u200dample.example", // a joiner where none may stand
		"https://пример\u2024example/":   "пример\u2024example",   // a one-dot leader is no dot
		"https://ｘｎ--evil-.example/":     "ｘｎ--evil-.example",     // an xn-- form that holds only ASCII
		"https://\xff.example/":          "\xff.example",          // not UTF-8
	} {
		wantErr(t, targetErr(raw, d, m), fmt.Sprintf("url: %q is not a valid host name", host))
	}

	// Through the API: stored, returned and followed in the ASCII form.
	s, _ := newTestServer(t)
	w := api(s, "POST", "/api/links", `{"url": "https://пример.example/a?b#c"}`)
	status(t, w, http.StatusCreated)
	link := linkIn(t, w)
	eq(t, link.URL, "https://xn--e1afmkfd.example/a?b#c")
	redirectsTo(t, do(s, "GET", "short.example", "/"+link.Code), "https://xn--e1afmkfd.example/a?b#c")
	w = api(s, "PATCH", "/api/links/"+link.Code, `{"url": "https://ｅｘａｍｐｌｅ.org/z"}`)
	status(t, w, http.StatusOK)
	eq(t, linkIn(t, w).URL, "https://example.org/z")
	redirectsTo(t, do(s, "GET", "short.example", "/"+link.Code), "https://example.org/z")
	for _, fields := range []map[string]any{
		{"text": "see https://пример.example/some/long/path now"},
		{"text": "see https://пример.example/some/long/path now", "urls": []string{"https://пример.example/some/long/path"}},
	} {
		got := shortened(t, s, fields)
		eq(t, len(got.Links), 1)
		eq(t, got.Links[0].URL, "https://xn--e1afmkfd.example/some/long/path")
		eq(t, got.Text, "see "+got.Links[0].ShortURL+" now")
	}
}

// Spellings a browser opens as a blocked host meet the block list: each is
// refused as blocked, or as no host name at all. So do spellings of one of
// the service's own domains, and of an IPv4 address.
func TestLookAlikeHostsMeetTheBlockList(t *testing.T) {
	m := targetMeta(t)
	d := m.Domain("short.example")
	must(t, m.Block("evil.example"))
	must(t, m.Block("xn--e1afmkfd.example")) // пример.example
	must(t, m.Block("192.0.2.1"))

	// Each spelling of a blocked host, bare, with a port and with user info.
	for _, host := range []string{
		"ｅｖｉｌ.example",         // full-width letters
		"ＥＶＩＬ.ｅｘａｍｐｌｅ",         // full-width capitals, the whole name
		"ⓔⓥⓘⓛ.example",         // circled letters
		"ev\u00adil.example",   // a soft hyphen inside the name, which a browser drops
		"evil\u200b.example",   // a zero-width space, dropped too
		"evil.example\u200b",   // the same at the end
		"evil。example",         // the ideographic full stop for a dot
		"evil．example",         // the full-width full stop
		"evil｡example",         // the halfwidth ideographic full stop
		"EVIL.example",         // upper case
		"Evil.Example.",        // a trailing dot
		"ｅｖｉｌ.example。",        // a trailing ideographic full stop
		"www.ｅｖｉｌ.example",     // a subdomain
		"пример.example",       // the Cyrillic spelling of a host blocked in its xn-- form
		"ПРИМЕР.example",       // in capitals
		"ｘｎ--e1afmkfd.example", // its xn-- form in full-width letters
		"XN--E1AFMKFD.EXAMPLE", // the xn-- form itself, in capitals
		"１９２.０.２.１",            // full-width digits
		"𝟏𝟗𝟐.𝟎.𝟐.𝟏",            // mathematical bold digits
	} {
		for _, raw := range []string{"https://" + host + "/", "https://" + host + ":8443/", "https://user:pw@" + host + "/x"} {
			wantErr(t, targetErr(raw, d, m), "is blocked")
		}
	}
	const notAName = "is not a valid host name"
	for raw, want := range map[string]string{
		"https://evil.example@ｅｖｉｌ.example/": "is blocked", // user info that looks like a host
		"https://a@b@ｅｖｉｌ.example/":          "is blocked", // the host follows the last @
		"https://ｅｖｉｌ.example#@x.example/":   "is blocked", // an @ after the host is not user info
		"https://ｅｖｉｌ.example：８４４３/":         notAName,     // a full-width colon is no port
		"https://user＠ｅｖｉｌ.example/":         notAName,     // a full-width @ is no user info
		"https://evil\ufe52example/":         notAName,     // the small full stop: refused, though a browser may read a dot
		"https://evil\u200d.example/":        notAName,     // a joiner where none may stand
		"https://ｅｖｉｌ\u2024example/":         notAName,     // a one-dot leader is no dot
		"https://١٩٢.٠.٢.١/":                 notAName,     // Arabic-Indic digits are no ASCII digits
		"https://０ｘｃ０.０.２.１/":                "four numbers",
		"https://３２２１２２５９８５/":                "four numbers",
		"https://１９２.０.２.１./":                "four numbers",
		"https://[::ffff:１９２.０.２.１]/":        "url must start with http:// or https://", // not an address url.Parse takes
	} {
		t.Run(raw, func(t *testing.T) {
			wantErr(t, targetErr(raw, d, m), want)
		})
	}
	// One of the service's own domains in full-width letters.
	for _, raw := range []string{"https://ｓｈｏｒｔ.example/x", "https://www.ｇｏ.example:443/x"} {
		wantErr(t, targetErr(raw, d, m), "url is already a short link on one of this service's domains")
	}
}

// A host name with an empty label, once one trailing dot is taken off, is
// refused, blocked or not: nothing can be looked up by such a name, and the
// block list would not see the name inside it. One trailing dot is fine.
func TestHostWithAnEmptyLabelIsRefused(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		m := targetMeta(t)
		d := m.Domain("short.example")
		if blocked {
			must(t, m.Block("evil.example"))
		}
		for _, host := range []string{
			"evil.example..", // two trailing dots
			"ｅｖｉｌ.example..", // the same in full-width letters
			"evil..example",  // two dots inside
			"evil.example.。", // a trailing dot and an ideographic full stop
			"evil。。example",  // two ideographic full stops
			".evil.example",  // a dot at the start
			"。evil.example",  // an ideographic full stop at the start
			".",              // nothing but the dot
			"..",
		} {
			for _, raw := range []string{"https://" + host + "/", "https://" + host + ":8443/", "https://user:pw@" + host + "/x"} {
				wantErr(t, targetErr(raw, d, m), fmt.Sprintf("url: %q is not a valid host name", host))
			}
		}
		target, err := CheckTarget("https://example.com./a", d, m)
		must(t, err)
		eq(t, target, "https://example.com./a")
	}
}

// A label that starts with xn-- must be the ASCII form of a name: what
// idna makes of it, unchanged. One that decodes to ASCII alone, or to
// nothing, is refused, blocked or not, since a reader that decodes it could
// reach a blocked host. The other labels of an ASCII host keep their rules.
func TestXNLabelMustBeTheFormOfAName(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		m := targetMeta(t)
		d := m.Domain("short.example")
		if blocked {
			must(t, m.Block("evil.example"))
			must(t, m.Block("192.0.2.1"))
		}
		for _, host := range []string{
			"xn--evil-.example", // decodes to evil.example
			"XN--EVIL-.EXAMPLE", // the same in capitals
			"evil.xn--example-", // decodes to evil.example
			"evil.example.xn--", // decodes to evil.example.
			"192.0.2.1.xn--",    // decodes to 192.0.2.1.
			"xn--.example",      // decodes to .example
		} {
			for _, raw := range []string{"https://" + host + "/", "https://" + host + ":8443/", "https://user:pw@" + host + "/x"} {
				wantErr(t, targetErr(raw, d, m), fmt.Sprintf("url: %q is not a valid host name", host))
			}
		}
	}

	m := targetMeta(t)
	d := m.Domain("short.example")
	for _, ok := range []string{"https://xn--e1afmkfd.example/", "https://XN--E1AFMKFD.EXAMPLE/", "https://a_b.xn--e1afmkfd.example/"} {
		target, err := CheckTarget(ok, d, m)
		must(t, err)
		eq(t, target, ok)
	}
	must(t, m.Block("xn--e1afmkfd.example"))
	for _, raw := range []string{"https://xn--e1afmkfd.example/", "https://XN--E1AFMKFD.EXAMPLE/"} {
		wantErr(t, targetErr(raw, d, m), `host "xn--e1afmkfd.example" is blocked`)
	}
}
