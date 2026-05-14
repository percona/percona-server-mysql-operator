package util

import (
	"os"

	"github.com/flosch/pongo2"
	"github.com/pkg/errors"
)

// SandboxedTemplateSet returns a pongo2 TemplateSet with file-access tags banned
// to prevent server-side template injection from user-controlled .spec.*.configuration fields.
func SandboxedTemplateSet() (*pongo2.TemplateSet, error) {
	set := pongo2.NewSet("sandbox", pongo2.MustNewLocalFileSystemLoader(os.TempDir()))
	for _, tag := range []string{"ssi", "include", "import", "extends"} {
		if err := set.BanTag(tag); err != nil {
			return nil, errors.Wrapf(err, "ban tag %q", tag)
		}
	}
	return set, nil
}