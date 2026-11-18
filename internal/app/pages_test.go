// Tests of reading the operator's own pages from a folder and from paths.
package app

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pagesFolder makes a folder that holds the given files.
func pagesFolder(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		writeFile(t, dir, name, content)
	}
	return dir
}

// pagesAre asserts each page's content; an empty want means no page.
func pagesAre(t *testing.T, pages *Pages, want map[string]string) {
	t.Helper()
	for name, content := range want {
		got := pages.page(name)
		if content == "" && got != nil {
			t.Errorf("page %q is %q, want none", name, got)
		}
		if string(got) != content {
			t.Errorf("page %q is %q, want %q", name, got, content)
		}
	}
}

// refused asserts that the pages do not load and that the error names what
// is wrong.
func pagesRefused(t *testing.T, cfg PagesConfig, fragments ...string) {
	t.Helper()
	_, err := LoadPages(cfg)
	if err == nil {
		t.Fatalf("LoadPages(%+v) gave no error", cfg)
	}
	for _, fragment := range fragments {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("the error %q does not say %q", err, fragment)
		}
	}
}

func TestLoadPagesFromAFolder(t *testing.T) {
	dir := pagesFolder(t, map[string]string{
		"index.html": "<p>home</p>", "404.html": "<p>none</p>", "429.html": "<p>slow</p>",
		"other.html": "<p>other</p>", "403.html": "<p>no</p>", "index": "no ending",
	})
	pages, err := LoadPages(PagesConfig{Dir: dir})
	must(t, err)
	pagesAre(t, pages, map[string]string{
		"index": "<p>home</p>", "404": "<p>none</p>", "429": "<p>slow</p>",
		"500": "", "other": "", "403": "", "index.html": "",
	})
}

func TestLoadPagesFilesWinOverTheFolder(t *testing.T) {
	dir := pagesFolder(t, map[string]string{"index.html": "folder index", "404.html": "folder 404"})
	own := writeFile(t, t.TempDir(), "missing.html", "own 404")
	pages, err := LoadPages(PagesConfig{Dir: dir, Files: map[string]string{"404": own}})
	must(t, err)
	pagesAre(t, pages, map[string]string{"index": "folder index", "404": "own 404"})

	// Without a folder a path is enough.
	pages, err = LoadPages(PagesConfig{Files: map[string]string{"500": own}})
	must(t, err)
	pagesAre(t, pages, map[string]string{"500": "own 404", "404": "", "index": ""})
}

// TestLoadPagesWithoutAnything: no pages are named, and none are there. A
// nil *Pages has none either.
func TestLoadPagesWithoutAnything(t *testing.T) {
	pages, err := LoadPages(PagesConfig{})
	must(t, err)
	var none *Pages
	for _, name := range pageNames {
		pagesAre(t, pages, map[string]string{name: ""})
		pagesAre(t, none, map[string]string{name: ""})
	}
}

// TestLoadPagesMissingPathIsNoError: a path that names no file gives the
// built-in page, not the folder's file of that name.
func TestLoadPagesMissingPathIsNoError(t *testing.T) {
	dir := pagesFolder(t, map[string]string{"index.html": "folder index", "404.html": "folder 404"})
	pages, err := LoadPages(PagesConfig{Dir: dir, Files: map[string]string{"404": filepath.Join(dir, "none.html")}})
	must(t, err)
	pagesAre(t, pages, map[string]string{"index": "folder index", "404": ""})
}

func TestLoadPagesMissingFolderIsNoError(t *testing.T) {
	pages, err := LoadPages(PagesConfig{Dir: filepath.Join(t.TempDir(), "none")})
	must(t, err)
	pagesAre(t, pages, map[string]string{"index": "", "404": ""})
}

// TestLoadPagesEmptyFileIsNoPage: a file without content, which is also
// what a file looks like while it is written again, is no page.
func TestLoadPagesEmptyFileIsNoPage(t *testing.T) {
	dir := pagesFolder(t, map[string]string{"index.html": "", "404.html": "<p>none</p>"})
	pages, err := LoadPages(PagesConfig{Dir: dir})
	must(t, err)
	pagesAre(t, pages, map[string]string{"index": "", "404": "<p>none</p>"})
}

// TestLoadPagesRefusesALinkOutOfTheFolder: the folder is never left. A
// link that stays inside it is followed.
func TestLoadPagesRefusesALinkOutOfTheFolder(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "secret.txt", "secret")
	for _, target := range []string{filepath.Join(outside, "secret.txt"), "../" + filepath.Base(outside) + "/secret.txt"} {
		dir := pagesFolder(t, map[string]string{"index.html": "home"})
		must(t, os.Symlink(target, filepath.Join(dir, "404.html")))
		pagesRefused(t, PagesConfig{Dir: dir}, "404.html")
	}

	dir := pagesFolder(t, map[string]string{"real.html": "inside"})
	must(t, os.Symlink("real.html", filepath.Join(dir, "404.html")))
	pages, err := LoadPages(PagesConfig{Dir: dir})
	must(t, err)
	pagesAre(t, pages, map[string]string{"404": "inside"})
}

// TestLoadPagesRefusesALinkOutOfAPathsFolder: a page that has a path of its
// own is read from the folder it is in, and that folder is not left either:
// whoever may write there cannot make another file a page.
func TestLoadPagesRefusesALinkOutOfAPathsFolder(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "secret.txt", "secret")
	for _, target := range []string{filepath.Join(outside, "secret.txt"), "../" + filepath.Base(outside) + "/secret.txt"} {
		dir := t.TempDir()
		link := filepath.Join(dir, "missing.html")
		must(t, os.Symlink(target, link))
		pagesRefused(t, PagesConfig{Files: map[string]string{"404": link}}, "missing.html")
	}

	dir := pagesFolder(t, map[string]string{"real.html": "inside"})
	link := filepath.Join(dir, "missing.html")
	must(t, os.Symlink("real.html", link))
	pages, err := LoadPages(PagesConfig{Files: map[string]string{"404": link}})
	must(t, err)
	pagesAre(t, pages, map[string]string{"404": "inside"})

	// A folder on the way to the file may be a link: the operator wrote
	// that path.
	through := filepath.Join(t.TempDir(), "pages")
	must(t, os.Symlink(dir, through))
	pages, err = LoadPages(PagesConfig{Files: map[string]string{"404": filepath.Join(through, "real.html")}})
	must(t, err)
	pagesAre(t, pages, map[string]string{"404": "inside"})
}

// TestLoadPagesNeverWaitsForAPipe: opening a pipe that nobody writes to
// waits for ever; a page that is one is refused at once.
func TestLoadPagesNeverWaitsForAPipe(t *testing.T) {
	dir := t.TempDir()
	pipe := filepath.Join(dir, "404.html")
	must(t, syscall.Mkfifo(pipe, 0o644))
	for _, cfg := range []PagesConfig{{Dir: dir}, {Files: map[string]string{"index": pipe}}} {
		done := make(chan error, 1)
		go func() {
			_, err := LoadPages(cfg)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "regular file") {
				t.Errorf("LoadPages(%+v) gave %v, want an error that says it is no regular file", cfg, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("LoadPages(%+v) still waits for the pipe after two seconds", cfg)
		}
	}
}

func TestLoadPagesRefusesALargeFile(t *testing.T) {
	largest := strings.Repeat("a", maxPageSize)
	dir := pagesFolder(t, map[string]string{"404.html": largest})
	pages, err := LoadPages(PagesConfig{Dir: dir})
	must(t, err)
	eq(t, len(pages.page("404")), maxPageSize)

	writeFile(t, dir, "404.html", largest+"a")
	pagesRefused(t, PagesConfig{Dir: dir}, "404.html", "1 MB")
	pagesRefused(t, PagesConfig{Files: map[string]string{"index": filepath.Join(dir, "404.html")}}, "404.html", "1 MB")
}

func TestLoadPagesRefusesWhatIsNoFile(t *testing.T) {
	dir := t.TempDir()
	must(t, os.Mkdir(filepath.Join(dir, "404.html"), 0o755))
	pagesRefused(t, PagesConfig{Dir: dir}, "404.html", "regular file")
	pagesRefused(t, PagesConfig{Files: map[string]string{"index": dir}}, dir, "regular file")

	// A folder that is a file is no folder.
	file := writeFile(t, t.TempDir(), "pages", "not a folder")
	pagesRefused(t, PagesConfig{Dir: file}, file)
}

func TestLoadPagesRefusesAFileItMayNotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	dir := pagesFolder(t, map[string]string{"404.html": "<p>none</p>"})
	must(t, os.Chmod(filepath.Join(dir, "404.html"), 0o000))
	pagesRefused(t, PagesConfig{Dir: dir}, "404.html")
}

// TestPageStampsChange: the stamps tell that a page file appeared, changed
// or went away, and follow the path of a page that has one.
func TestPageStampsChange(t *testing.T) {
	dir := t.TempDir()
	cfg := PagesConfig{Dir: dir}
	none := pageStamps(cfg)
	if none != pageStamps(PagesConfig{}) {
		t.Fatal("an empty folder has stamps other than no folder")
	}

	path := writeFile(t, dir, "404.html", "one")
	added := pageStamps(cfg)
	if added == none {
		t.Fatal("a file that appeared left the stamps as they were")
	}
	writeFile(t, dir, "404.html", "longer")
	if pageStamps(cfg) == added {
		t.Fatal("a file that changed left the stamps as they were")
	}
	writeFile(t, dir, "other.html", "not a page")
	must(t, os.Remove(path))
	if pageStamps(cfg) != none {
		t.Fatal("after the file went away the stamps are not those of an empty folder")
	}

	own := writeFile(t, t.TempDir(), "own.html", "own")
	withPath := PagesConfig{Dir: dir, Files: map[string]string{"404": own}}
	before := pageStamps(withPath)
	writeFile(t, dir, "404.html", "the folder's file, which the path hides")
	if pageStamps(withPath) != before {
		t.Fatal("the folder's file changed the stamps of a page that has a path")
	}
	writeFile(t, filepath.Dir(own), "own.html", "own, longer")
	if pageStamps(withPath) == before {
		t.Fatal("the file of the path changed and the stamps did not")
	}
}
