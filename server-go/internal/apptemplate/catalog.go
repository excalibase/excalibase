package apptemplate

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed builtin/*.yaml builtin/catalog/*.yaml
var builtinFiles embed.FS

// Catalog is a read-only set of templates, in a fixed order.
type Catalog struct {
	ordered []*Template
	byID    map[string]*Template
}

// NewCatalog refuses two templates with one id.
func NewCatalog(templates ...*Template) (*Catalog, error) {
	c := &Catalog{byID: make(map[string]*Template, len(templates))}
	for _, tpl := range templates {
		if c.byID[tpl.ID] != nil {
			return nil, fmt.Errorf("%w: two templates are named %q", ErrInvalidTemplate, tpl.ID)
		}
		c.byID[tpl.ID] = tpl
		c.ordered = append(c.ordered, tpl)
	}
	return c, nil
}

func (c *Catalog) List() []*Template { return append([]*Template{}, c.ordered...) }

func (c *Catalog) Get(id string) (*Template, bool) {
	tpl, ok := c.byID[id]
	return tpl, ok
}

// Builtins are the templates the platform ships, the starters first and then
// the catalog (EXC-669); a file that does not parse stops the server.
func Builtins() (*Catalog, error) {
	starters, err := readBuiltins("builtin/*.yaml", false)
	if err != nil {
		return nil, err
	}
	catalog, err := readBuiltins("builtin/catalog/*.yaml", true)
	if err != nil {
		return nil, err
	}
	return NewCatalog(append(starters, catalog...)...)
}

func readBuiltins(pattern string, inCatalog bool) ([]*Template, error) {
	names, err := fs.Glob(builtinFiles, pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	templates := make([]*Template, 0, len(names))
	for _, name := range names {
		source, err := builtinFiles.ReadFile(name)
		if err != nil {
			return nil, err
		}
		tpl, err := Parse(source)
		if err != nil {
			return nil, fmt.Errorf("built-in template %s: %w", name, err)
		}
		tpl.Catalog = inCatalog
		templates = append(templates, tpl)
	}
	return templates, nil
}
