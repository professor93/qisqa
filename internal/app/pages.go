// The operator's own pages: HTML files that take the place of the built-in
// index page and of the answers 404, 429 and 500 in a browser.
package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// PagesConfig names the operator's own pages: a folder that holds them
// under fixed names, and a path for one page, which wins over the folder.
type PagesConfig struct {
	Dir   string            `yaml:"dir"`
	Files map[string]string `yaml:"files"`
}

// pageNames are the pages an operator can replace: the index page, and the
// statuses the service answers to a browser. In the folder each is the file
// of that name with ".html". No other file is ever opened.
var pageNames = [...]string{"index", "404", "429", "500"}

// maxPageSize is the largest page in bytes: every page is held in memory
// and sent whole.
const maxPageSize = 1 << 20

// check refuses a path for something that is no page, and an empty path.
func (c PagesConfig) check() error {
	for name, path := range c.Files {
		if !slices.Contains(pageNames[:], name) {
			return fmt.Errorf("pages.files: %q is no page; the pages are %s", name, strings.Join(pageNames[:], ", "))
		}
		if path == "" {
			return fmt.Errorf("pages.files.%s: the path is empty", name)
		}
	}
	return nil
}

// Pages holds the operator's pages in memory, in the order of pageNames.
// Nobody changes one after it is loaded.
type Pages struct {
	html [len(pageNames)][]byte
}

// page returns the operator's page of that name, or nil when there is
// none: the built-in answer is used then.
func (p *Pages) page(name string) []byte {
	if p == nil {
		return nil
	}
	for i, known := range pageNames {
		if known == name {
			return p.html[i]
		}
	}
	return nil
}

// pageFiles is where a page is read from: the folder, which is never left,
// or the file system as it is, for a path the operator wrote.
type pageFiles interface {
	Stat(name string) (fs.FileInfo, error)
	Open(name string) (*os.File, error)
}

// anyFile reads a path as it is written.
type anyFile struct{}

func (anyFile) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }
func (anyFile) Open(name string) (*os.File, error)    { return os.Open(name) }

// LoadPages reads the pages cfg names into memory. A file that is not
// there, or that is empty, is no page, and neither is a folder that is not
// there: the built-in answer stays. Anything else that cannot be a page is
// an error: a file larger than maxPageSize, what is no regular file, a
// file that cannot be read, and in the folder a link that leads out of it.
func LoadPages(cfg PagesConfig) (*Pages, error) {
	var folder *os.Root
	if cfg.Dir != "" {
		root, err := os.OpenRoot(cfg.Dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			slog.Warn("pages.dir is not there, the built-in pages are used", "dir", cfg.Dir)
		case err != nil:
			return nil, fmt.Errorf("pages.dir: %w", err)
		default:
			folder = root
			defer folder.Close()
		}
	}

	pages := &Pages{}
	for i, name := range pageNames {
		var (
			data []byte
			err  error
		)
		path := cfg.Files[name]
		switch {
		case path != "":
			data, err = readPage(anyFile{}, path)
			if errors.Is(err, fs.ErrNotExist) {
				slog.Warn("pages.files names a file that is not there, the built-in page is used", "page", name, "path", path)
				continue
			}
		case folder != nil:
			path = filepath.Join(cfg.Dir, name+".html")
			data, err = readPage(folder, name+".html")
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
		default:
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("page %s: %w", path, err)
		}
		if len(data) > 0 {
			pages.html[i] = data
		}
	}
	return pages, nil
}

// readPage reads one page. It looks before it opens: opening what is no
// regular file can wait forever.
func readPage(files pageFiles, name string) ([]byte, error) {
	info, err := files.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("is not a regular file")
	}
	if info.Size() > maxPageSize {
		return nil, errors.New("is larger than 1 MB")
	}
	f, err := files.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// The file may have grown since it was looked at.
	data, err := io.ReadAll(io.LimitReader(f, maxPageSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPageSize {
		return nil, errors.New("is larger than 1 MB")
	}
	return data, nil
}

// pageStamps returns, in the order of pageNames, the stamp of the file each
// page would be read from: a change of any of them means the pages are to
// be read again.
func pageStamps(cfg PagesConfig) (stamps [len(pageNames)]fileStamp) {
	for i, name := range pageNames {
		switch {
		case cfg.Files[name] != "":
			stamps[i] = stampOf(cfg.Files[name])
		case cfg.Dir != "":
			stamps[i] = stampOf(filepath.Join(cfg.Dir, name+".html"))
		}
	}
	return stamps
}
