// The operator's own pages: HTML files that take the place of the built-in
// index page and of the answers 404, 429 and 500 in a browser.
package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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

// count returns how many pages there are.
func (p *Pages) count() (n int) {
	if p == nil {
		return 0
	}
	for _, page := range p.html {
		if page != nil {
			n++
		}
	}
	return n
}

// LoadPages reads the pages cfg names into memory. A file that is not
// there, or that is empty, is no page, and neither is a folder that is not
// there: the built-in answer stays. Anything else that cannot be a page is
// an error: a file larger than maxPageSize, what is no regular file, a
// file that cannot be read, and a link that leads out of the folder the
// file is in.
//
// Every page is read through the folder it is in, which is never left: the
// folder of the config, or the folder of a path. So whoever may write there
// cannot make a file from elsewhere, one of qisqa's own, a page.
func LoadPages(cfg PagesConfig) (*Pages, error) {
	pages := &Pages{}
	for i, name := range pageNames {
		dir, file := cfg.Dir, name+".html"
		if path := cfg.Files[name]; path != "" {
			dir, file = filepath.Dir(path), filepath.Base(path)
		}
		if dir == "" {
			continue
		}
		data, err := readPage(dir, file)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("page %s: %w", filepath.Join(dir, file), err)
		}
		if len(data) > 0 {
			pages.html[i] = data
		}
	}
	return pages, nil
}

// readPage reads the file of that name in the folder dir. It opens without
// waiting and looks at what it opened: a pipe would make a plain open wait
// for ever, and a look before the open could see another file.
func readPage(dir, file string) ([]byte, error) {
	folder, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer folder.Close()
	f, err := folder.OpenFile(file, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("is not a regular file")
	}
	// One byte more than a page may have tells a file that is too large,
	// also one that grows while it is read.
	data, err := io.ReadAll(io.LimitReader(f, maxPageSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPageSize {
		return nil, errors.New("is larger than 1 MB")
	}
	return data, nil
}

// missingPages returns, for the log, what cfg names that is not there: the
// folder, and each path of a page.
func missingPages(cfg PagesConfig) (missing []string) {
	notThere := func(path string) bool {
		_, err := os.Stat(path)
		return errors.Is(err, fs.ErrNotExist)
	}
	if cfg.Dir != "" && notThere(cfg.Dir) {
		missing = append(missing, cfg.Dir)
	}
	for _, name := range pageNames {
		if path := cfg.Files[name]; path != "" && notThere(path) {
			missing = append(missing, path)
		}
	}
	return missing
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
