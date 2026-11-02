package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
)

type projectSymbol struct {
	name     string
	location location
}

type documentSymbol struct {
	Name           string           `json:"name"`
	Kind           int              `json:"kind"`
	SelectionRange *sourceRange     `json:"selectionRange"`
	Location       *location        `json:"location"`
	Children       []documentSymbol `json:"children"`
}

// JDT workspace/symbol only searches types. Document outlines also include
// methods, constructors and fields; cache them until that file's next change.
func (s *session) symbols(ctx context.Context, q query) ([]destination, error) {
	if s.symbolIndex == nil {
		s.symbolIndex = make(map[string][]projectSymbol)
	}
	files := s.watcher.sourceFiles()
	present := make(map[string]bool, len(files)+len(q.Documents))
	for _, name := range files {
		present[name] = true
	}
	for name := range q.Documents {
		if sourceFile(name) && !present[name] {
			files = append(files, name)
			present[name] = true
		}
	}
	sort.Strings(files)
	for name := range s.symbolIndex {
		if !present[name] {
			delete(s.symbolIndex, name)
		}
	}
	search := strings.ToLower(strings.TrimSpace(q.Search))
	type match struct {
		symbol projectSymbol
		rank   int
	}
	matches := make([]match, 0)
	for _, name := range files {
		if kotlinFile(name) != s.kotlin || (buildFile(name) && name != q.Path) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		included, err := projectSymbolDirectory(filepath.Dir(name), s.projectRoots)
		if err != nil {
			return nil, err
		}
		if !included {
			continue
		}
		symbols, cached := s.symbolIndex[name]
		if !cached {
			result, err := s.rpc.call(ctx, "textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": fileURI(name)}})
			if err != nil {
				return nil, fmt.Errorf("index %s: %w", name, err)
			}
			var outline []documentSymbol
			if err := json.Unmarshal(result, &outline); err != nil {
				return nil, err
			}
			symbols, err = flattenSymbols(outline, name)
			if err != nil {
				return nil, err
			}
			s.symbolIndex[name] = symbols
		}
		for _, symbol := range symbols {
			if rank := symbolRank(symbol.name, search); rank >= 0 {
				matches = append(matches, match{symbol, rank})
			}
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].rank < matches[j].rank })
	out := make([]destination, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, match := range matches {
		targets, err := destinations([]location{match.symbol.location}, "symbol", q, seen)
		if err != nil {
			return nil, err
		}
		if len(targets) != 0 {
			targets[0].Name = match.symbol.name
			out = append(out, targets[0])
		}
	}
	return out, nil
}

func flattenSymbols(outline []documentSymbol, path string) ([]projectSymbol, error) {
	out := make([]projectSymbol, 0)
	var visit func([]documentSymbol) error
	visit = func(items []documentSymbol) error {
		for _, item := range items {
			// Package/module headings aren't navigable declarations.
			if item.Kind != 1 && item.Kind != 2 && item.Kind != 3 && item.Kind != 4 {
				var loc location
				if item.SelectionRange != nil {
					loc = location{URI: fileURI(path), Range: *item.SelectionRange}
				} else if item.Location != nil {
					loc = *item.Location
					u, err := url.Parse(loc.URI)
					if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
						continue
					}
					name, err := canonical(u.Path)
					if err != nil {
						return err
					}
					if name != path {
						continue
					}
					loc.URI = fileURI(name)
				} else {
					return fmt.Errorf("symbol %q has no source location", item.Name)
				}
				out = append(out, projectSymbol{name: item.Name, location: loc})
			}
			if err := visit(item.Children); err != nil {
				return err
			}
		}
		return nil
	}
	err := visit(outline)
	return out, err
}

// Exact names, prefixes and substrings precede subsequence matches. The query
// matches declaration names, never comments or arbitrary body text.
func symbolRank(name, query string) int {
	name = strings.ToLower(name)
	base, _, _ := strings.Cut(name, "(")
	if name == query || base == query {
		return 0
	}
	if strings.HasPrefix(name, query) {
		return 1
	}
	if strings.Contains(name, query) {
		return 2
	}
	remaining := []rune(query)
	for _, ch := range name {
		if len(remaining) != 0 && ch == remaining[0] {
			remaining = remaining[1:]
		}
	}
	if len(remaining) == 0 {
		return 3
	}
	return -1
}
