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

func TestReadmeCoversEndpoints(t *testing.T) {
	readme := readDoc(t, "README.md")
	for _, want := range []string{
		"POST /api/links", "PATCH /api/links/", "DELETE /api/links/",
		"/api/llms.txt", "/api/openapi.yaml", "/healthz",
		"expires_after_open", "password", "config.example.yaml",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not mention %q", want)
		}
	}
}

// TestReadmeConfigTable checks that the README's configuration table has a
// row for every top-level key of the example config.
func TestReadmeConfigTable(t *testing.T) {
	var keys yaml.Node
	must(t, yaml.Unmarshal([]byte(readDoc(t, "config.example.yaml")), &keys))
	readme := readDoc(t, "README.md")
	mapping := keys.Content[0]
	for i := 0; i < len(mapping.Content); i += 2 {
		if key := mapping.Content[i].Value; !strings.Contains(readme, "| `"+key) {
			t.Errorf("README.md has no configuration row for %q", key)
		}
	}
}

// TestReadmeCoversAdminCommands: every admin command is described in the
// README, by the first two words that name it.
func TestReadmeCoversAdminCommands(t *testing.T) {
	readme := readDoc(t, "README.md")
	for _, c := range adminCommands {
		if !strings.Contains(readme, c.name()) {
			t.Errorf("README.md does not mention the admin command %q", c.name())
		}
	}
}

// TestReadmeCoversDataFolder: the README says what the data folder holds and
// how to run qisqa in a small container.
func TestReadmeCoversDataFolder(t *testing.T) {
	readme := readDoc(t, "README.md")
	for _, want := range []string{"`LOCK`", "`key`", "`meta.json`", ".chunk", "`GOMEMLIMIT`"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not mention %s", want)
		}
	}
}

// TestReadmeCoversSyncEvery: the README says what each setting of
// sync_every answers after, what can be lost and when, that codes are never
// handed out twice, and the numbers a stop that was not in order passes
// over; and what an interval adds when the disk fails.
func TestReadmeCoversSyncEvery(t *testing.T) {
	readme := readDoc(t, "README.md")
	_, section, ok := strings.Cut(readme, "### When a change is on disk")
	section, _, _ = strings.Cut(section, "\n## ")
	if !ok {
		t.Fatal("README.md has no section ### When a change is on disk")
	}
	section = strings.Join(strings.Fields(section), " ")
	for _, want := range []string{
		"`sync_every: 0`", "once the change is synced", "Nothing it answered is lost",
		"`100ms` to `1m`", "answers at once", "loses power", "the changes of the last interval can be lost",
		"never handed out twice", strconv.Itoa(reserveAhead/1000) + "," + fmt.Sprintf("%03d", reserveAhead%1000),
		"stop in order", "loses nothing",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("README.md's section on sync_every does not say %q", want)
		}
	}
	_, disk, _ := strings.Cut(readme, "**When the disk fails.**")
	disk, _, _ = strings.Cut(disk, "\n- **")
	disk = strings.Join(strings.Fields(disk), " ")
	for _, want := range []string{"`sync_every`", "may not be on disk", "`since_last_good_sync`"} {
		if !strings.Contains(disk, want) {
			t.Errorf("README.md's bullet on a failing disk does not say %q", want)
		}
	}
}

// TestReadmeCoversRestore: the README says how to put a copy of the data
// folder back with -skip, and its command for the compose file keeps the
// flags of the image's CMD, which the words after the service name replace.
func TestReadmeCoversRestore(t *testing.T) {
	readme := readDoc(t, "README.md")
	for _, want := range []string{"-skip", "`skipped`"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not mention %s", want)
		}
	}
	// A code turned down for a word in it uses a number too, so a count of
	// the links made since the copy is too small for N.
	_, restore, ok := strings.Cut(readme, "**Restoring a copy.**")
	restore, _, _ = strings.Cut(restore, "\n- **")
	if !ok {
		t.Error("README.md has no bullet **Restoring a copy.**")
	}
	for _, want := range []string{"`N` counts numbers", "turned down", "add a tenth"} {
		if !strings.Contains(strings.Join(strings.Fields(restore), " "), want) {
			t.Errorf("README.md's bullet on restoring a copy does not say %q", want)
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
	for line := range strings.Lines(readme) {
		if strings.Contains(line, "docker compose run") && strings.Contains(line, "-skip") {
			command = strings.TrimSpace(line)
		}
	}
	switch {
	case command == "":
		t.Error("README.md gives no docker compose run command with -skip")
	case !strings.Contains(command, " app "+strings.Join(cmd, " ")+" -skip "):
		t.Errorf("README.md's %q drops the image's CMD %q", command, cmd)
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
