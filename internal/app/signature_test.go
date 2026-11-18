// Tests of the author's signature: the header on every response and the
// comment at the end of every HTML page.
package app

import (
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestEveryResponseIsSigned: whatever the service answers, a page, a
// redirect, JSON, a text file or a refusal, carries the signature header
// once.
func TestEveryResponseIsSigned(t *testing.T) {
	s, _ := newTestServer(t)
	code, _ := live(t, s)
	type answerOf struct {
		name   string
		w      *httptest.ResponseRecorder
		status int
	}
	answers := []answerOf{
		{"the health check", do(s, "GET", "short.example", "/healthz"), http.StatusOK},
		{"robots.txt", do(s, "GET", "short.example", "/robots.txt"), http.StatusOK},
		{"the index page", do(s, "GET", "short.example", "/"), http.StatusOK},
		{"a redirect", do(s, "GET", "short.example", "/"+code), http.StatusFound},
		{"the fallback of a domain", do(s, "GET", "go.example", "/"), http.StatusFound},
		{"an unknown path", do(s, "GET", "short.example", "/a/b"), http.StatusNotFound},
		{"a created link", api(s, "POST", "/api/links", longURLBody), http.StatusCreated},
		{"a request without a token", do(s, "POST", "short.example", "/api/links", withBody(longURLBody)), http.StatusUnauthorized},
		{"an unknown API endpoint", do(s, "GET", "short.example", "/api/none"), http.StatusNotFound},
		{"the description of the API", do(s, "GET", "short.example", "/api/openapi.yaml"), http.StatusOK},
	}
	// Last, the answers that count against the client, until it is refused.
	for i := 0; ; i++ {
		w := do(s, "GET", "short.example", fmt.Sprintf("/none%d", i))
		if w.Code == http.StatusTooManyRequests {
			answers = append(answers, answerOf{"a blocked client", w, http.StatusTooManyRequests})
			break
		}
		if i == 20 {
			t.Fatal("twenty unknown codes did not get the client blocked")
		}
		answers = append(answers, answerOf{"an unknown code", w, http.StatusNotFound})
	}
	if len(answers) < 12 {
		t.Fatalf("only %d answers: no unknown code was answered before the client was blocked", len(answers))
	}

	for _, answer := range answers {
		if answer.w.Code != answer.status {
			t.Errorf("%s: status %d, want %d", answer.name, answer.w.Code, answer.status)
		}
		if got := answer.w.Header().Values(signatureHeader); len(got) != 1 || got[0] != signature {
			t.Errorf("%s: the header %s is %q, want %q once", answer.name, signatureHeader, got, signature)
		}
	}
}

// TestEveryHTMLPageIsSigned: each page the service shows ends with the
// signature as an HTML comment, and there is no page this test does not
// open.
func TestEveryHTMLPageIsSigned(t *testing.T) {
	s, _ := newTestServer(t)
	open, _ := secured(t, s, time.Minute)
	locked, _ := passwordLink(t, s, nil)
	pages := map[string]string{
		"index.html":    "/",
		"404.html":      "/none1",
		"open.html":     "/" + open,
		"password.html": "/" + locked,
	}
	files, err := fs.Glob(webFS, "web/*.html")
	must(t, err)
	for _, file := range files {
		if _, ok := pages[strings.TrimPrefix(file, "web/")]; !ok {
			t.Errorf("%s is a page that this test does not open", file)
		}
	}
	eq(t, len(files), len(pages))

	for name, path := range pages {
		w := do(s, "GET", "short.example", path)
		page, err := webFS.ReadFile("web/" + name)
		must(t, err)
		if contentType := w.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
			t.Errorf("%s: Content-Type %q, want text/html", name, contentType)
		}
		if got, want := w.Body.String(), string(page)+"<!-- "+signature+" -->\n"; got != want {
			t.Errorf("%s: the page ends with %q, want the file and then the signature comment", name, got[max(0, len(got)-60):])
		}
	}
}

// TestSignatureFitsAHeaderAndAComment: the text can stand in a header line
// and cannot end the comment it stands in.
func TestSignatureFitsAHeaderAndAComment(t *testing.T) {
	if signature == "" || strings.TrimSpace(signature) != signature {
		t.Fatalf("the signature %q is empty or has space around it", signature)
	}
	for _, r := range signature {
		if r < ' ' || r == 0x7f {
			t.Fatalf("the signature %q has a control character, which no header line may hold", signature)
		}
	}
	if strings.Contains(signature, "--") || strings.ContainsAny(signature, "<>") {
		t.Fatalf("the signature %q has \"--\", \"<\" or \">\", which would break the HTML comment", signature)
	}
}
