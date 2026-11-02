package main

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
)

type sessionPool map[bool]*session

func kotlinFile(path string) bool { return filepath.Ext(path) == ".kt" || filepath.Ext(path) == ".kts" }

func (pool sessionPool) close() {
	for key, server := range pool {
		server.close()
		delete(pool, key)
	}
}

func (pool sessionPool) run(ctx context.Context, root string, q query, pos position) ([]destination, error) {
	key := kotlinFile(q.Path)
	current := pool[key]
	if current == nil {
		var err error
		current, err = newSession(ctx, root, q)
		if err != nil {
			return nil, err
		}
		pool[key] = current
	}
	// Preview reads are cached in a request-local document map. They must not
	// become editor overlays when the second language is queried.
	primary := q
	primary.Documents = cloneDocuments(q.Documents)
	out, err := current.navigate(ctx, primary, pos)
	if err != nil || q.Action != "symbols" {
		return out, err
	}
	otherPath := ""
	files := current.watcher.sourceFiles()
	for path := range q.Documents {
		files = append(files, path)
	}
	for _, path := range files {
		if !sourceFile(path) || buildFile(path) || kotlinFile(path) == key {
			continue
		}
		included, err := projectSymbolDirectory(filepath.Dir(path), []string{root})
		if err != nil {
			return nil, err
		}
		if included {
			otherPath = path
			break
		}
	}
	if otherPath == "" {
		return out, nil
	}
	peerQuery := q
	peerQuery.Path = otherPath
	peerQuery.Documents = cloneDocuments(q.Documents)
	peer := pool[!key]
	if peer == nil {
		peer, err = newSession(ctx, root, peerQuery)
		if err != nil {
			return nil, err
		}
		pool[!key] = peer
	}
	more, err := peer.navigate(ctx, peerQuery, position{})
	if err != nil {
		return nil, err
	}
	out = append(out, more...)
	search := strings.ToLower(q.Search)
	sort.SliceStable(out, func(i, j int) bool { return symbolRank(out[i].Name, search) < symbolRank(out[j].Name, search) })
	return out, nil
}

func cloneDocuments(documents map[string]string) map[string]string {
	copy := make(map[string]string, len(documents))
	for path, text := range documents {
		copy[path] = text
	}
	return copy
}
