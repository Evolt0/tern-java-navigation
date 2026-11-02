package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

type fileChange struct {
	URI  string `json:"uri"`
	Type int    `json:"type"`
}

func buildFile(path string) bool {
	switch filepath.Base(path) {
	case "pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts", "gradle.properties", "gradle-wrapper.properties", "maven.config", "jvm.config", "extensions.xml", ".project", ".classpath":
		return true
	}
	return strings.HasSuffix(path, ".gradle") || strings.HasSuffix(path, ".gradle.kts") || (filepath.Ext(path) == ".prefs" && filepath.Base(filepath.Dir(path)) == ".settings")
}

func sourceFile(path string) bool {
	switch filepath.Ext(path) {
	case ".java", ".kt", ".kts":
		return true
	}
	return false
}

func relevantFile(path string) bool { return sourceFile(path) || buildFile(path) }

func ignoredDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", ".gradle", ".m2", ".metadata", ".idea", ".cache", "target", "build", "out", "bin":
		return true
	}
	return false
}

func (f *fileWatcher) register(raw json.RawMessage) error {
	var params struct {
		Registrations []struct {
			Method          string
			RegisterOptions struct {
				Watchers []struct{ GlobPattern json.RawMessage }
			}
		}
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return err
	}
	for _, registration := range params.Registrations {
		if registration.Method != "workspace/didChangeWatchedFiles" {
			continue
		}
		for _, watcher := range registration.RegisterOptions.Watchers {
			var pattern string
			if err := json.Unmarshal(watcher.GlobPattern, &pattern); err != nil {
				var relative struct {
					BaseURI json.RawMessage `json:"baseUri"`
					Pattern string
				}
				if err := json.Unmarshal(watcher.GlobPattern, &relative); err != nil {
					return err
				}
				var base string
				if err := json.Unmarshal(relative.BaseURI, &base); err != nil {
					var folder struct{ URI string }
					if err := json.Unmarshal(relative.BaseURI, &folder); err != nil {
						return err
					}
					base = folder.URI
				}
				u, err := url.Parse(base)
				if err != nil || u.Scheme != "file" {
					return fmt.Errorf("unsupported watch base URI %q", base)
				}
				pattern = filepath.Join(u.Path, relative.Pattern)
			}
			if !filepath.IsAbs(pattern) {
				pattern = filepath.Join(f.root, pattern)
			}
			prefix := pattern
			if i := strings.IndexAny(prefix, "*?{["); i >= 0 {
				prefix = prefix[:i]
			}
			root := filepath.Dir(prefix)
			if strings.HasSuffix(prefix, string(filepath.Separator)) {
				root = filepath.Clean(prefix)
			}
			if err := f.add(root, strings.Contains(pattern, "**"), false); err != nil {
				return err
			}
		}
	}
	return nil
}
