package main

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
)

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// Prefer a Gradle settings root or a Maven reactor that explicitly enrolls the
// nearest module. An unrelated ancestor pom must not absorb a nested project.
func workspace(path string) string {
	start := filepath.Dir(path)
	project, source, repository := "", "", ""
	maven := false
	for dir := start; ; dir = filepath.Dir(dir) {
		if regularFile(filepath.Join(dir, "settings.gradle")) || regularFile(filepath.Join(dir, "settings.gradle.kts")) {
			if project == "" || !maven {
				return dir
			}
		}
		pom := filepath.Join(dir, "pom.xml")
		if regularFile(pom) {
			if project == "" {
				project, maven = dir, true
			} else if maven && mavenIncludes(pom, project) {
				project = dir
			}
		}
		if project == "" && (regularFile(filepath.Join(dir, "build.gradle")) || regularFile(filepath.Join(dir, "build.gradle.kts")) || regularFile(filepath.Join(dir, ".project"))) {
			project = dir
		}
		if source == "" && filepath.Base(dir) == "src" {
			source = filepath.Dir(dir)
		}
		if repository == "" {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				repository = dir
			}
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	if project != "" {
		return project
	}
	if source != "" {
		return source
	}
	if repository != "" {
		return repository
	}
	return start
}

func mavenIncludes(pom, module string) bool {
	data, err := os.ReadFile(pom)
	if err != nil {
		return false
	}
	var project struct {
		Modules []string `xml:"modules>module"`
	}
	if xml.Unmarshal(data, &project) != nil {
		return false
	}
	for _, member := range project.Modules {
		path, err := canonical(filepath.Join(filepath.Dir(pom), strings.TrimSpace(member)))
		if err == nil && path == module {
			return true
		}
	}
	return false
}

func projectSymbolDirectory(dir string, roots []string) (bool, error) {
	for _, root := range roots {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return false, err
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			switch part {
			case ".git", ".hg", ".gradle", ".idea", "node_modules", "target", "build", "out", "bin":
				return false, nil
			}
		}
		return true, nil
	}
	return false, nil
}
