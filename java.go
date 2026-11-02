package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type javaLaunch struct {
	executable string
	args       []string
	env        []string
	sources    string
}

const jdtMinimumJavaMajor = 21

func javaMajor(executable string) int {
	output, err := exec.Command(executable, "-version").CombinedOutput()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(output), "\n") {
		index := strings.Index(line, `version "`)
		if index < 0 {
			continue
		}
		version := line[index+len(`version "`):]
		if end := strings.IndexByte(version, '"'); end >= 0 {
			version = version[:end]
		}
		if dot := strings.IndexByte(version, '.'); dot >= 0 {
			version = version[:dot]
		}
		if major, err := strconv.Atoi(version); err == nil {
			return major
		}
	}
	return 0
}

func firstCompatibleJava(candidates []string, minimum int) string {
	// Prefer the newest runtime: JDT LS 1.63 needs Java 25 while older releases
	// still run on 21, and a newer JVM satisfies both.
	best, bestMajor := "", 0
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		if major := javaMajor(candidate); major >= minimum && major > bestMajor {
			best, bestMajor = candidate, major
		}
	}
	return best
}

func javaEnvironment() ([]string, string, error) {
	env := os.Environ()
	if home := os.Getenv("JDTLS_JAVA_HOME"); home != "" {
		java := filepath.Join(home, "bin", "java")
		if major := javaMajor(java); major < jdtMinimumJavaMajor {
			return nil, "", fmt.Errorf("JDTLS_JAVA_HOME contains Java %d; JDT LS 1.63+ requires Java %d or newer", major, jdtMinimumJavaMajor)
		}
		return addJavaPath(env, java), java, nil
	}
	candidates := make([]string, 0, 6)
	if home := os.Getenv("JAVA_HOME"); home != "" {
		candidates = append(candidates, filepath.Join(home, "bin", "java"))
	}
	if found, err := exec.LookPath("java"); err == nil {
		candidates = append(candidates, found)
	}
	candidates = append(candidates,
		"/opt/homebrew/opt/openjdk/bin/java", "/opt/homebrew/opt/openjdk@25/bin/java",
		"/usr/local/opt/openjdk/bin/java", "/usr/local/opt/openjdk@25/bin/java", "/usr/bin/java")
	java := firstCompatibleJava(candidates, jdtMinimumJavaMajor)
	if java == "" {
		return nil, "", fmt.Errorf("JDT LS requires Java %d or newer; install it or set JDTLS_JAVA_HOME (checked JAVA_HOME, PATH and common Homebrew locations)", jdtMinimumJavaMajor)
	}
	return addJavaPath(env, java), java, nil
}

func addJavaPath(env []string, java string) []string {
	// GUI applications often omit Homebrew, including the Python interpreter
	// used by the upstream jdtls launcher. Preserve the user's PATH precedence.
	path := os.Getenv("PATH")
	if dir := filepath.Dir(java); dir != "" {
		path = dir + string(os.PathListSeparator) + path
	}
	path += string(os.PathListSeparator) + "/opt/homebrew/bin" + string(os.PathListSeparator) + "/usr/local/bin"
	return replaceEnvironment(env, "PATH", path)
}

func replaceEnvironment(env []string, key, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func discoverJDTLS(root string) (javaLaunch, error) {
	env, java, err := javaEnvironment()
	if err != nil {
		return javaLaunch{}, err
	}
	state, err := daemonDirectory(root)
	if err != nil {
		return javaLaunch{}, err
	}
	data, configuration, sources := filepath.Join(state, "data"), filepath.Join(state, "configuration"), filepath.Join(state, "sources")
	for _, dir := range []string{data, configuration, sources} {
		if err := privateDir(dir); err != nil {
			return javaLaunch{}, err
		}
	}
	launch := javaLaunch{env: env, sources: sources}
	if override := os.Getenv("JDTLS"); override != "" {
		launch.executable, err = exec.LookPath(override)
		if err != nil {
			return javaLaunch{}, fmt.Errorf("JDTLS executable %q: %w", override, err)
		}
	} else if home := os.Getenv("JDTLS_HOME"); home != "" {
		return distributionLaunch(home, java, data, configuration, launch)
	} else if found, err := exec.LookPath("jdtls"); err == nil {
		launch.executable = found
	} else {
		for _, candidate := range []string{"/opt/homebrew/bin/jdtls", "/usr/local/bin/jdtls"} {
			if found, err := exec.LookPath(candidate); err == nil {
				launch.executable = found
				break
			}
		}
		if launch.executable == "" {
			for _, home := range []string{"/opt/homebrew/opt/jdtls/libexec", "/usr/local/opt/jdtls/libexec"} {
				if info, err := os.Stat(home); err == nil && info.IsDir() {
					return distributionLaunch(home, java, data, configuration, launch)
				}
			}
		}
	}
	if launch.executable == "" {
		return javaLaunch{}, errors.New("Eclipse JDT LS not found: install jdtls (brew install jdtls), set JDTLS to its launcher, or set JDTLS_HOME to its extracted distribution")
	}
	launch.args = []string{"--java-executable", java, "-data", data, "-configuration", configuration}
	return launch, nil
}

func distributionLaunch(home, java, data, configuration string, launch javaLaunch) (javaLaunch, error) {
	home, err := filepath.Abs(home)
	if err != nil {
		return javaLaunch{}, err
	}
	jars, err := filepath.Glob(filepath.Join(home, "plugins", "org.eclipse.equinox.launcher_*.jar"))
	if err != nil {
		return javaLaunch{}, err
	}
	plain := filepath.Join(home, "plugins", "org.eclipse.equinox.launcher.jar")
	if info, err := os.Stat(plain); err == nil && !info.IsDir() {
		jars = []string{plain}
	}
	if len(jars) != 1 {
		return javaLaunch{}, fmt.Errorf("JDTLS_HOME %s must contain exactly one Equinox launcher jar (found %d)", home, len(jars))
	}
	platform := map[string]string{"darwin": "mac", "linux": "linux", "windows": "win"}[runtime.GOOS]
	if platform == "" {
		return javaLaunch{}, fmt.Errorf("JDT LS does not support platform %s", runtime.GOOS)
	}
	configSource := filepath.Join(home, "config_"+platform)
	if runtime.GOARCH == "arm64" {
		arm := configSource + "_arm"
		if info, err := os.Stat(arm); err == nil && info.IsDir() {
			configSource = arm
		}
	}
	if err := copyJDTConfiguration(configSource, configuration); err != nil {
		return javaLaunch{}, fmt.Errorf("JDT LS configuration: %w", err)
	}
	launch.executable = java
	launch.args = []string{
		"-Declipse.application=org.eclipse.jdt.ls.core.id1", "-Dosgi.bundles.defaultStartLevel=4",
		"-Declipse.product=org.eclipse.jdt.ls.core.product", "-Dosgi.checkConfiguration=true", "-Xms256m", "-Xmx1g",
		"--add-modules=ALL-SYSTEM", "--add-opens", "java.base/java.util=ALL-UNNAMED", "--add-opens", "java.base/java.lang=ALL-UNNAMED",
		"-jar", jars[0], "-configuration", configuration, "-data", data,
	}
	return launch, nil
}

func copyJDTConfiguration(source, destination string) error {
	if _, err := os.Stat(filepath.Join(source, "config.ini")); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink in JDT LS configuration: %s", path)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func javaSettings() map[string]any {
	gradle := map[string]any{"enabled": true, "wrapper": map[string]any{"enabled": true}}
	// JDT LS itself runs on Java 25, while Gradle 8.x cannot run on 25.
	// Buildship otherwise launches Gradle with the language server's JVM.
	if home := os.Getenv("JAVA_HOME"); home != "" {
		gradle["java"] = map[string]any{"home": home}
	}
	return map[string]any{"java": map[string]any{
		"import":        map[string]any{"maven": map[string]any{"enabled": true}, "gradle": gradle, "exclusions": []string{"**/node_modules/**", "**/.git/**", "**/target/**", "**/build/**"}},
		"configuration": map[string]any{"updateBuildConfiguration": "automatic"},
		"autobuild":     map[string]any{"enabled": true},
		"search":        map[string]any{"scope": "projectOnly"},
		// Keep dependency definitions, but do not scan decompiled JDK/library internals for usages.
		"references": map[string]any{"includeDecompiledSources": false},
	}}
}

func decodeLocations(raw json.RawMessage) ([]location, error) {
	var entries []json.RawMessage
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		entries = []json.RawMessage{raw}
	} else if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	locations := make([]location, 0, len(entries))
	for _, entry := range entries {
		var value struct {
			URI                  string       `json:"uri"`
			Range                sourceRange  `json:"range"`
			TargetURI            string       `json:"targetUri"`
			TargetSelectionRange *sourceRange `json:"targetSelectionRange"`
		}
		if err := json.Unmarshal(entry, &value); err != nil {
			return nil, err
		}
		if value.TargetURI != "" {
			if value.TargetSelectionRange == nil {
				return nil, errors.New("JDT LS returned a LocationLink without targetSelectionRange")
			}
			locations = append(locations, location{URI: value.TargetURI, Range: *value.TargetSelectionRange})
		} else {
			if value.URI == "" {
				return nil, errors.New("JDT LS returned a location without a URI")
			}
			locations = append(locations, location{URI: value.URI, Range: value.Range})
		}
	}
	return locations, nil
}

func (s *session) materializeLocations(ctx context.Context, locations []location) ([]location, error) {
	// Cache only for this session: source attachments can change on disk.
	for i := range locations {
		u, err := url.Parse(locations[i].URI)
		if err != nil {
			return nil, err
		}
		if u.Scheme != "jdt" && u.Scheme != "jar" {
			continue
		}
		uri := locations[i].URI
		path, found := s.classFiles[uri]
		if !found {
			var source []byte
			var name string
			if u.Scheme == "jdt" {
				raw, err := s.rpc.call(ctx, "java/classFileContents", map[string]any{"uri": uri})
				if err != nil {
					return nil, fmt.Errorf("Java dependency source unavailable for %s: %w", uri, err)
				}
				var text string
				if err := json.Unmarshal(raw, &text); err != nil || strings.TrimSpace(text) == "" {
					return nil, fmt.Errorf("Java dependency source unavailable for %s: JDT LS returned no class-file contents; attach the dependency sources", uri)
				}
				source = []byte(text)
				name = filepath.Base(u.Path)
				if !strings.HasSuffix(name, ".java") {
					name = strings.TrimSuffix(name, ".class") + ".java"
				}
			} else {
				name, source, err = archiveSource(uri)
				if err != nil {
					return nil, fmt.Errorf("dependency source unavailable for %s: %w", uri, err)
				}
			}
			if s.sourceCache == "" {
				return nil, errors.New("dependency source cache is unavailable")
			}
			digest := sha256.Sum256([]byte(uri))
			dir := filepath.Join(s.sourceCache, fmt.Sprintf("%x", digest[:16]))
			if err := os.MkdirAll(dir, 0700); err != nil {
				return nil, err
			}
			path = filepath.Join(dir, name)
			if err := os.WriteFile(path, source, 0600); err != nil {
				return nil, err
			}
			if s.classFiles == nil {
				s.classFiles = make(map[string]string)
			}
			s.classFiles[uri] = path
		}
		// Preserve the server's UTF-16 range and the exact source bytes.
		locations[i].URI = fileURI(path)
	}
	return locations, nil
}

func archiveSource(uri string) (string, []byte, error) {
	archiveURI, member, ok := strings.Cut(strings.TrimPrefix(uri, "jar:"), "!/")
	if !ok {
		return "", nil, errors.New("unsupported archive URI: missing !/ member separator")
	}
	u, err := url.Parse(archiveURI)
	if err != nil {
		return "", nil, err
	}
	if u.Scheme != "file" && u.Scheme != "" || u.Host != "" && u.Host != "localhost" {
		return "", nil, errors.New("archive source must be a local file")
	}
	path := filepath.FromSlash(u.Path)
	if !filepath.IsAbs(path) {
		return "", nil, errors.New("archive source path must be absolute")
	}
	member, err = url.PathUnescape(member)
	if err != nil {
		return "", nil, err
	}
	if !fs.ValidPath(member) {
		return "", nil, errors.New("invalid archive source member")
	}
	if !sourceFile(member) {
		return "", nil, errors.New("archive target is not source text; attach the dependency's .java or .kt sources")
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		return "", nil, err
	}
	defer archive.Close()
	file, err := archive.Open(member)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	const limit = 32 << 20
	source, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return "", nil, err
	}
	if len(source) > limit {
		return "", nil, errors.New("dependency source exceeds 32 MiB")
	}
	return filepath.Base(member), source, nil
}

func discoverKotlinLS(root string) (javaLaunch, error) {
	state, err := daemonDirectory(root)
	if err != nil {
		return javaLaunch{}, err
	}
	system := filepath.Join(state, "kotlin")
	if err := privateDir(system); err != nil {
		return javaLaunch{}, err
	}
	sources := filepath.Join(state, "kotlin-sources")
	if err := privateDir(sources); err != nil {
		return javaLaunch{}, err
	}
	launch := javaLaunch{env: os.Environ(), sources: sources}
	if override := os.Getenv("KOTLIN_LSP"); override != "" {
		launch.executable, err = exec.LookPath(override)
		if err != nil {
			return javaLaunch{}, fmt.Errorf("KOTLIN_LSP executable %q: %w", override, err)
		}
	} else {
		var candidates []string
		if home := os.Getenv("KOTLIN_LSP_HOME"); home != "" {
			candidates = []string{filepath.Join(home, "bin", "intellij-server"), filepath.Join(home, "bin", "kotlin-lsp"), filepath.Join(home, "kotlin-lsp.sh")}
		} else {
			candidates = []string{"kotlin-lsp", "intellij-server", "/opt/homebrew/bin/kotlin-lsp", "/usr/local/bin/kotlin-lsp", "/opt/homebrew/opt/kotlin-lsp/libexec/bin/intellij-server", "/usr/local/opt/kotlin-lsp/libexec/bin/intellij-server"}
			if home, err := os.UserHomeDir(); err == nil {
				candidates = append(candidates,
					filepath.Join(home, ".local", "bin", "kotlin-lsp"),
					filepath.Join(home, ".local", "share", "kotlin-lsp", "current", "bin", "intellij-server"))
			}
		}
		for _, candidate := range candidates {
			if executable, err := exec.LookPath(candidate); err == nil {
				launch.executable = executable
				break
			}
		}
	}
	if launch.executable == "" {
		return javaLaunch{}, errors.New("JetBrains Kotlin LSP not found: install kotlin-lsp, set KOTLIN_LSP to its launcher, or set KOTLIN_LSP_HOME to its distribution")
	}
	launch.args = []string{"--stdio", "--system-path=" + system}
	return launch, nil
}
