package rebac

import (
	_ "embed"
	"fmt"
	"io/fs"
	"path"
	"sort"

	"core/authz/rebac/engine"
)

// the base module: user and tenant with its roles (owner, admin, member, viewer)
//
//go:embed base.fga
var baseModule string

// Modules returns the module files of a model: the base module (user, tenant)
// and the app's *.fga files at the root of app (e.g. an embed.FS). Names are
// stable ("base.fga", "app/<file>") so the same files give the same source.
func Modules(app fs.FS) ([]engine.ModuleFile, error) {
	paths, err := fs.Glob(app, "*.fga")
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("rebac: no module (*.fga) in the app's model files")
	}
	sort.Strings(paths)
	files := []engine.ModuleFile{{Name: "base.fga", Contents: baseModule}}
	for _, p := range paths {
		b, err := fs.ReadFile(app, p)
		if err != nil {
			return nil, err
		}
		files = append(files, engine.ModuleFile{Name: path.Join("app", p), Contents: string(b)})
	}
	return files, nil
}
