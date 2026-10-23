package service

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"

	"authz/internal/engine"
)

// ReadModules reads the module files of app's model:
//
//	<platformDir>/*.fga         the platform module (user, tenant), platform/authz/model
//	<appsDir>/<app>/authz/*.fga the app's module(s), apps/<app>/authz
//
// Files are named "platform/<file>" and "apps/<app>/authz/<file>" whatever the
// directories, so the same files give the same model source wherever they are
// mounted (no new version on a mere move).
func ReadModules(platformDir, appsDir, app string) ([]engine.ModuleFile, error) {
	platform, err := readFGA(platformDir, "platform")
	if err != nil {
		return nil, err
	}
	if len(platform) == 0 {
		return nil, fmt.Errorf("no platform module (*.fga) in %s", platformDir)
	}
	appDir := filepath.Join(appsDir, app, "authz")
	own, err := readFGA(appDir, path.Join("apps", app, "authz"))
	if err != nil {
		return nil, err
	}
	if len(own) == 0 {
		return nil, fmt.Errorf("app %s has no authorization module (*.fga) in %s", app, appDir)
	}
	return append(platform, own...), nil
}

// readFGA reads dir/*.fga, naming each file prefix/<file>.
func readFGA(dir, prefix string) ([]engine.ModuleFile, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.fga"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	files := make([]engine.ModuleFile, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		files = append(files, engine.ModuleFile{Name: path.Join(prefix, filepath.Base(p)), Contents: string(b)})
	}
	return files, nil
}
