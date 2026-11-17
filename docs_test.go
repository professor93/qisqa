// Tests that keep the documents in step with the program.
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// readDoc returns the content of a document in the repository.
func readDoc(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	must(t, err)
	return string(data)
}

// docFiles returns README.md and every page of docs/.
func docFiles(t *testing.T) []string {
	t.Helper()
	pages, err := filepath.Glob("docs/*.md")
	must(t, err)
	return append([]string{"README.md"}, pages...)
}

// readDocs returns README.md and every page of docs/, joined.
func readDocs(t *testing.T) string {
	t.Helper()
	var docs strings.Builder
	for _, name := range docFiles(t) {
		docs.WriteString(readDoc(t, name))
		docs.WriteString("\n")
	}
	return docs.String()
}

// headingLevel returns the level of a Markdown heading line, 1 for "# ",
// or 0 for a line that is no heading.
func headingLevel(line string) int {
	level := len(line) - len(strings.TrimLeft(line, "#"))
	if level < 1 || level > 6 || !strings.HasPrefix(line[level:], " ") {
		return 0
	}
	return level
}

// prose returns the lines of a Markdown document outside its fenced code
// blocks, where a "# " starts a comment rather than a heading.
func prose(doc string) []string {
	var lines []string
	fenced := false
	for line := range strings.Lines(doc) {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		} else if !fenced {
			lines = append(lines, line)
		}
	}
	return lines
}

// section returns the part of a Markdown document under the heading line
// heading, up to the next heading of the same or a higher level.
func section(doc, heading string) (string, bool) {
	level := headingLevel(heading)
	var part strings.Builder
	found, fenced := false, false
	for line := range strings.Lines(doc) {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		} else if l := headingLevel(line); !fenced && l > 0 {
			if found && l <= level {
				break
			}
			if !found && strings.TrimSpace(line) == heading {
				found = true
				continue
			}
		}
		if found {
			part.WriteString(line)
		}
	}
	return part.String(), found
}

// githubAnchor returns the anchor GitHub gives a heading: its text in lower
// case, each space turned into "-", punctuation dropped.
func githubAnchor(heading string) string {
	var anchor strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case r == ' ':
			anchor.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r):
			anchor.WriteRune(r)
		}
	}
	return anchor.String()
}

// anchors returns the anchors of the headings of a Markdown document. A
// heading whose anchor an earlier one has gets "-1", "-2" and so on after
// it, as on GitHub.
func anchors(doc string) map[string]bool {
	found := make(map[string]bool)
	seen := make(map[string]int)
	for _, line := range prose(doc) {
		level := headingLevel(line)
		if level == 0 {
			continue
		}
		anchor := githubAnchor(line[level:])
		if n := seen[anchor]; n > 0 {
			found[fmt.Sprintf("%s-%d", anchor, n)] = true
		} else {
			found[anchor] = true
		}
		seen[anchor]++
	}
	return found
}

// markdownLink is an inline link, [text](target); it captures the target.
var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)

// relativeLinks returns the targets of the links of a Markdown document that
// lead into the repository rather than to a site, as written.
func relativeLinks(doc string) []string {
	var targets []string
	for _, match := range markdownLink.FindAllStringSubmatch(strings.Join(prose(doc), ""), -1) {
		if target := match[1]; !strings.Contains(target, "://") && !strings.HasPrefix(target, "mailto:") {
			targets = append(targets, target)
		}
	}
	return targets
}

// TestDocLinksLeadSomewhere: every link of README.md and of the pages of
// docs/ that leads into the repository names a file that is there, and its
// #fragment, if it has one, a heading of that file, by the anchor GitHub
// gives the heading.
func TestDocLinksLeadSomewhere(t *testing.T) {
	// The anchor rule, on headings of this repository and the anchors
	// GitHub gave them.
	for heading, want := range map[string]string{
		"Create a link: `POST /api/links`":                  "create-a-link-post-apilinks",
		"Edit a link: `PATCH /api/links/{code}`":            "edit-a-link-patch-apilinkscode",
		"Shorten the URLs inside a text: `POST /api/links`": "shorten-the-urls-inside-a-text-post-apilinks",
		"When a change is on disk":                          "when-a-change-is-on-disk",
		"Accounts and domains":                              "accounts-and-domains",
	} {
		eq(t, githubAnchor(heading), want)
	}

	links := 0
	for _, name := range docFiles(t) {
		for _, target := range relativeLinks(readDoc(t, name)) {
			links++
			file, fragment, _ := strings.Cut(target, "#")
			path := name
			if file != "" {
				path = filepath.Join(filepath.Dir(name), file)
			}
			if !filepath.IsLocal(path) {
				t.Errorf("%s: the link %q leads out of the repository", name, target)
				continue
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Errorf("%s: the link %q names %s, which is not there", name, target, path)
				continue
			}
			if fragment == "" {
				continue
			}
			if info.IsDir() || filepath.Ext(path) != ".md" {
				t.Errorf("%s: the link %q has a fragment, but %s is no Markdown file", name, target, path)
			} else if !anchors(readDoc(t, path))[fragment] {
				t.Errorf("%s: the link %q names no heading of %s", name, target, path)
			}
		}
	}
	if links == 0 {
		t.Error("the documents have no link into the repository: the pattern of a link is wrong")
	}
}

// TestEveryDocPageIsListed: every page of docs/ is linked from the list of
// pages, docs/README.md, and from the section Documentation of README.md.
func TestEveryDocPageIsListed(t *testing.T) {
	documentation, ok := section(readDoc(t, "README.md"), "## Documentation")
	if !ok {
		t.Error("README.md has no section ## Documentation")
	}
	pages, err := filepath.Glob("docs/*.md")
	must(t, err)
	for _, list := range []struct{ name, dir, doc string }{
		{"docs/README.md", "docs", readDoc(t, "docs/README.md")},
		{"the section Documentation of README.md", ".", documentation},
	} {
		linked := make(map[string]bool)
		for _, target := range relativeLinks(list.doc) {
			file, _, _ := strings.Cut(target, "#")
			linked[filepath.Join(list.dir, file)] = true
		}
		for _, page := range pages {
			if page != "docs/README.md" && !linked[page] {
				t.Errorf("%s does not link to %s", list.name, page)
			}
		}
	}
}

// openAPI is the part of web/openapi.yaml the tests look at.
type openAPI struct {
	OpenAPI    string                    `yaml:"openapi"`
	Paths      map[string]map[string]any `yaml:"paths"`
	Components struct {
		Schemas map[string]struct {
			Properties map[string]any `yaml:"properties"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

func readOpenAPI(t *testing.T) openAPI {
	t.Helper()
	var doc openAPI
	must(t, yaml.Unmarshal([]byte(readDoc(t, "web/openapi.yaml")), &doc))
	return doc
}

// jsonFields returns the sorted JSON field names of a struct.
func jsonFields(v any) []string {
	var names []string
	for field := range reflect.TypeOf(v).Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func TestDocsEndpoints(t *testing.T) {
	s, _ := newTestServer(t)

	w := do(s, "GET", "short.example", "/api/llms.txt") // no token
	status(t, w, http.StatusOK)
	eq(t, w.Header().Get("Content-Type"), "text/plain; charset=utf-8")
	if !strings.HasPrefix(w.Body.String(), "# qisqa") {
		t.Fatalf("llms.txt starts with %.40q", w.Body.String())
	}
	eq(t, w.Body.String(), readDoc(t, "web/llms.txt"))

	w = do(s, "GET", "short.example", "/api/openapi.yaml")
	status(t, w, http.StatusOK)
	eq(t, w.Header().Get("Content-Type"), "application/yaml")
	eq(t, w.Body.String(), readDoc(t, "web/openapi.yaml"))
}

func TestOpenAPIMatchesRoutes(t *testing.T) {
	doc := readOpenAPI(t)
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Fatalf("openapi version %q, want 3.1.x", doc.OpenAPI)
	}

	var operations []string
	for path, item := range doc.Paths {
		for method := range item {
			if method != "parameters" { // shared parameters, not an operation
				operations = append(operations, strings.ToUpper(method)+" "+path)
			}
		}
	}
	slices.Sort(operations)
	want := []string{"DELETE /api/links/{code}", "PATCH /api/links/{code}", "POST /api/links"}
	if !slices.Equal(operations, want) {
		t.Fatalf("documented operations %v, want %v", operations, want)
	}

	// Every documented operation is a real route behind the token check.
	s, _ := newTestServer(t)
	for i, operation := range operations {
		method, path, _ := strings.Cut(operation, " ")
		path = strings.ReplaceAll(path, "{code}", "aB3xK")
		w := do(s, method, "short.example", path, withRemote(fmt.Sprintf("203.0.113.%d:1234", 100+i)))
		status(t, w, http.StatusUnauthorized)
	}
}

func TestOpenAPISchemasMatchTypes(t *testing.T) {
	schemas := readOpenAPI(t).Components.Schemas
	for name, value := range map[string]any{
		"CreateRequest": createRequest{},
		"PatchRequest":  patchRequest{},
		"Link":          linkResponse{},
		"TextResult":    textResponse{},
		"Error": struct {
			Error string `json:"error"`
		}{},
	} {
		schema, ok := schemas[name]
		if !ok {
			t.Fatalf("schema %s is missing", name)
		}
		var documented []string
		for property := range schema.Properties {
			documented = append(documented, property)
		}
		slices.Sort(documented)
		if want := jsonFields(value); !slices.Equal(documented, want) {
			t.Fatalf("schema %s documents %v, the program uses %v", name, documented, want)
		}
	}
}

func TestDocsMentionEveryField(t *testing.T) {
	fields := strings.Fields("url text urls domain expires_in expires_after_open password format " +
		"code short_url expires_at secured created error links")
	for _, name := range []string{"web/llms.txt", "web/openapi.yaml"} {
		doc := readDoc(t, name)
		for _, field := range fields {
			if !regexp.MustCompile(`\b` + field + `\b`).MatchString(doc) {
				t.Errorf("%s does not mention %q", name, field)
			}
		}
	}
}

// TestRepositoryUsesOnlyExampleDomains reads every text file of the
// repository: a URL in code, tests or documents may only name a domain
// reserved for examples.
func TestRepositoryUsesOnlyExampleDomains(t *testing.T) {
	urls := regexp.MustCompile(`https?://([A-Za-z0-9.-]+)`)
	fullName := regexp.MustCompile(`\.[a-z]{2,}$`) // "www" alone is code, not a host
	allowed := func(host string) bool {
		return host == "github.com" ||
			host == "example.com" || strings.HasSuffix(host, ".example.com") ||
			host == "example.org" || strings.HasSuffix(host, ".example.org") ||
			strings.HasSuffix(host, ".example")
	}
	// Hidden directories hold tooling and local state, not the project;
	// .github is the exception. dist and logs are build and runtime output.
	skipped := func(name string) bool {
		return name == "dist" || name == "logs" ||
			(strings.HasPrefix(name, ".") && name != "." && name != ".github")
	}

	must(t, filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipped(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || !utf8.Valid(data) {
			return err // a binary file holds no documentation
		}
		for _, match := range urls.FindAllSubmatch(data, -1) {
			host := strings.ToLower(strings.TrimRight(string(match[1]), "."))
			if fullName.MatchString(host) && !allowed(host) {
				t.Errorf("%s uses host %q; only example domains are allowed", path, host)
			}
		}
		return nil
	}))
}

func TestEveryGoFileSaysWhatItIsFor(t *testing.T) {
	files, err := filepath.Glob("*.go")
	must(t, err)
	for _, name := range files {
		data, err := os.ReadFile(name)
		must(t, err)
		if !strings.HasPrefix(string(data), "// ") {
			t.Errorf("%s does not start with a comment that says what the file is for", name)
		}
	}
}

// compose is the part of docker-compose.yml the tests look at.
type compose struct {
	Services map[string]struct {
		Image   string   `yaml:"image"`
		Ports   []string `yaml:"ports"`
		Restart string   `yaml:"restart"`
	} `yaml:"services"`
}

func readCompose(t *testing.T) compose {
	t.Helper()
	var doc compose
	must(t, yaml.Unmarshal([]byte(readDoc(t, "docker-compose.yml")), &doc))
	return doc
}

// TestComposePublishesOnLoopbackOnly keeps what docker-compose.yml starts
// reachable from this machine only: qisqa belongs behind a proxy. Docker
// publishes a port that is given without an address on every network
// interface.
func TestComposePublishesOnLoopbackOnly(t *testing.T) {
	services := readCompose(t).Services
	if len(services["app"].Ports) == 0 {
		t.Error("the app service publishes no port")
	}
	for name, service := range services {
		for _, port := range service.Ports {
			if !strings.HasPrefix(port, "127.0.0.1:") {
				t.Errorf("%s port %q is published on every network interface", name, port)
			}
		}
	}
}

// TestComposeRestartsTheService: qisqa ends itself when the disk fails, so
// that it starts again from what is on disk; something must start it again.
func TestComposeRestartsTheService(t *testing.T) {
	eq(t, readCompose(t).Services["app"].Restart, "unless-stopped")
}

// TestComposeRunsOnlyTheApp: qisqa needs no other service, since it keeps its links in its own data folder.
func TestComposeRunsOnlyTheApp(t *testing.T) {
	services := readCompose(t).Services
	if len(services) != 1 {
		t.Errorf("docker-compose.yml has %d services, want only app", len(services))
	}
	if _, ok := services["app"]; !ok {
		t.Errorf("docker-compose.yml has no service named app")
	}
}

// TestCIHasNoServices: the tests need nothing but Go, on any one machine.
func TestCIHasNoServices(t *testing.T) {
	workflow := readDoc(t, ".github/workflows/ci.yml")
	for _, word := range []string{"services:", "matrix:"} {
		if strings.Contains(workflow, word) {
			t.Errorf("the CI workflow has %q", word)
		}
	}
}

func TestDocsCoverEndpoints(t *testing.T) {
	docs := readDocs(t)
	for _, want := range []string{
		"POST /api/links", "PATCH /api/links/", "DELETE /api/links/",
		"/api/llms.txt", "/api/openapi.yaml", "/healthz",
		"expires_after_open", "password", "config.example.yaml",
	} {
		if !strings.Contains(docs, want) {
			t.Errorf("the documents do not mention %q", want)
		}
	}
}

// TestConfigurationTable checks that the configuration table of
// docs/configuration.md has a row for every top-level key of the example
// config.
func TestConfigurationTable(t *testing.T) {
	var keys yaml.Node
	must(t, yaml.Unmarshal([]byte(readDoc(t, "config.example.yaml")), &keys))
	page := readDoc(t, "docs/configuration.md")
	mapping := keys.Content[0]
	for i := 0; i < len(mapping.Content); i += 2 {
		if key := mapping.Content[i].Value; !strings.Contains(page, "| `"+key) {
			t.Errorf("docs/configuration.md has no configuration row for %q", key)
		}
	}
}

// TestDocsCoverAdminCommands: every admin command is described in the
// documents, by the first two words that name it.
func TestDocsCoverAdminCommands(t *testing.T) {
	docs := readDocs(t)
	for _, c := range adminCommands {
		if !strings.Contains(docs, c.name()) {
			t.Errorf("the documents do not mention the admin command %q", c.name())
		}
	}
}

// TestDocsCoverDataFolder: the documents say what the data folder holds and
// how to run qisqa in a small container.
func TestDocsCoverDataFolder(t *testing.T) {
	docs := readDocs(t)
	for _, want := range []string{"`LOCK`", "`key`", "`meta.json`", ".chunk", "`GOMEMLIMIT`"} {
		if !strings.Contains(docs, want) {
			t.Errorf("the documents do not mention %s", want)
		}
	}
}

// TestDocsCoverSyncEvery: the documents say what each setting of
// sync_every answers after, what can be lost and when, that codes are never
// handed out twice, and the numbers a stop that was not in order passes
// over; and what an interval adds when the disk fails.
func TestDocsCoverSyncEvery(t *testing.T) {
	docs := readDocs(t)
	part, ok := section(docs, "## When a change is on disk")
	if !ok {
		t.Fatal("the documents have no section ## When a change is on disk")
	}
	part = strings.Join(strings.Fields(part), " ")
	for _, want := range []string{
		"`sync_every: 0`", "once the change is synced", "Nothing it answered is lost",
		"`100ms` to `1m`", "answers at once", "loses power", "the changes of the last interval can be lost",
		"never handed out twice", strconv.Itoa(reserveAhead/1000) + "," + fmt.Sprintf("%03d", reserveAhead%1000),
		"stop in order", "loses nothing",
	} {
		if !strings.Contains(part, want) {
			t.Errorf("the section on sync_every does not say %q", want)
		}
	}
	_, disk, _ := strings.Cut(docs, "**When the disk fails.**")
	disk, _, _ = strings.Cut(disk, "\n- **")
	disk = strings.Join(strings.Fields(disk), " ")
	for _, want := range []string{"`sync_every`", "may not be on disk", "`since_last_good_sync`"} {
		if !strings.Contains(disk, want) {
			t.Errorf("the bullet on a failing disk does not say %q", want)
		}
	}
}

// TestDocsCoverRestore: the documents say how to put a copy of the data
// folder back with -skip, and their command for the compose file keeps the
// flags of the image's CMD, which the words after the service name replace.
func TestDocsCoverRestore(t *testing.T) {
	docs := readDocs(t)
	for _, want := range []string{"-skip", "`skipped`"} {
		if !strings.Contains(docs, want) {
			t.Errorf("the documents do not mention %s", want)
		}
	}
	// A code turned down for a word in it uses a number too, so a count of
	// the links made since the copy is too small for N.
	_, restore, ok := strings.Cut(docs, "**Restoring a copy.**")
	restore, _, _ = strings.Cut(restore, "\n- **")
	if !ok {
		t.Error("the documents have no bullet **Restoring a copy.**")
	}
	for _, want := range []string{"`N` counts numbers", "turned down", "add a tenth"} {
		if !strings.Contains(strings.Join(strings.Fields(restore), " "), want) {
			t.Errorf("the bullet on restoring a copy does not say %q", want)
		}
	}

	var cmd []string
	for line := range strings.Lines(readDoc(t, "Dockerfile")) {
		if args, ok := strings.CutPrefix(strings.TrimSpace(line), "CMD "); ok {
			must(t, json.Unmarshal([]byte(args), &cmd))
		}
	}
	if len(cmd) == 0 {
		t.Fatal("the Dockerfile has no CMD in the form of a JSON list")
	}
	var command string
	for line := range strings.Lines(docs) {
		if strings.Contains(line, "docker compose run") && strings.Contains(line, "-skip") {
			command = strings.TrimSpace(line)
		}
	}
	switch {
	case command == "":
		t.Error("the documents give no docker compose run command with -skip")
	case !strings.Contains(command, " app "+strings.Join(cmd, " ")+" -skip "):
		t.Errorf("the documents' %q drops the image's CMD %q", command, cmd)
	}
}

// TestGoModRequiresOnlyTheseModules: beside the standard library qisqa
// needs the YAML parser for its config and the IDNA tables for target
// hosts, and the text tables that come with those.
func TestGoModRequiresOnlyTheseModules(t *testing.T) {
	var direct, indirect []string
	inBlock := false
	for line := range strings.Lines(readDoc(t, "go.mod")) {
		line = strings.TrimSpace(line)
		switch {
		case line == "require (":
			inBlock = true
			continue
		case line == ")":
			inBlock = false
			continue
		case strings.HasPrefix(line, "require "):
			line = strings.TrimPrefix(line, "require ")
		case !inBlock:
			continue
		}
		module, _, _ := strings.Cut(line, " ")
		if strings.HasSuffix(line, "// indirect") {
			indirect = append(indirect, module)
		} else {
			direct = append(direct, module)
		}
	}
	slices.Sort(direct)
	eq(t, strings.Join(direct, " "), "go.yaml.in/yaml/v3 golang.org/x/net")
	eq(t, strings.Join(indirect, " "), "golang.org/x/text")
}

// TestImageKeepsTheLinksInTheVolume: the image takes relative paths from /
// and mounts its volume at /data, so the example config's data_dir, ./data,
// is the volume.
func TestImageKeepsTheLinksInTheVolume(t *testing.T) {
	const why = "WORKDIR /, VOLUME /data and the example's data_dir: ./data go together: " +
		"changing one without the others puts the links outside the volume, where they are lost with the container"

	// What the final image gets: a FROM starts a stage afresh.
	var workdir string
	var volumes []string
	for line := range strings.Lines(readDoc(t, "Dockerfile")) {
		instruction, args, _ := strings.Cut(strings.TrimSpace(line), " ")
		args = strings.TrimSpace(args)
		switch strings.ToUpper(instruction) {
		case "FROM":
			workdir, volumes = "", nil
		case "WORKDIR":
			workdir = args
		case "VOLUME":
			if strings.HasPrefix(args, "[") {
				var list []string
				must(t, json.Unmarshal([]byte(args), &list))
				volumes = append(volumes, list...)
			} else {
				volumes = append(volumes, strings.Fields(args)...)
			}
		}
	}
	if workdir != "/" {
		t.Errorf("the image works in %q, not /; %s", workdir, why)
	}
	if !slices.Contains(volumes, "/data") {
		t.Errorf("the image has the volumes %q, not /data; %s", volumes, why)
	}

	var example struct {
		DataDir string `yaml:"data_dir"`
	}
	must(t, yaml.Unmarshal([]byte(readDoc(t, "config.example.yaml")), &example))
	if example.DataDir != "./data" {
		t.Errorf("config.example.yaml has data_dir %q, not ./data; %s", example.DataDir, why)
	}
}
