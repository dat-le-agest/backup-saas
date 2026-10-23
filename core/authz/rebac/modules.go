package rebac

import (
	_ "embed"
	"fmt"
	"io/fs"
	"path"
	"sort"

	"core/authz/rebac/engine"
)

// the base model: user and tenant with its roles (owner, admin, member, viewer)
//
//go:embed base.yml
var baseModel string

// Modules returns the files of a service's model: the base model (user,
// tenant) and the *.yml / *.yaml files at the root of app (e.g. an embed.FS).
// Names are stable ("base.yml", "app/<file>") so the same files give the same source.
func Modules(app fs.FS) ([]engine.ModuleFile, error) {
	var paths []string
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		found, err := fs.Glob(app, pattern)
		if err != nil {
			return nil, err
		}
		paths = append(paths, found...)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("rebac: no model file (*.yml) in the service's model files")
	}
	sort.Strings(paths)
	files := []engine.ModuleFile{{Name: "base.yml", Contents: baseModel}}
	for _, p := range paths {
		b, err := fs.ReadFile(app, p)
		if err != nil {
			return nil, err
		}
		files = append(files, engine.ModuleFile{Name: path.Join("app", p), Contents: string(b)})
	}
	return files, nil
}

// LoadModel composes and validates the model of app's files with the base model.
func LoadModel(app fs.FS) (*engine.Model, error) {
	files, err := Modules(app)
	if err != nil {
		return nil, err
	}
	m, _, err := engine.ParseModules(files)
	return m, err
}

// BaseModel is the base model alone (user, tenant roles, member management),
// e.g. for core/authn's member checks.
func BaseModel() (*engine.Model, error) {
	m, _, err := engine.ParseModules([]engine.ModuleFile{{Name: "base.yml", Contents: baseModel}})
	return m, err
}
