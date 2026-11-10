// Package config holds cave's on-disk layout and its small JSON database
// of engines, prefixes and games.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Paths is the directory layout under the cave root.
type Paths struct {
	Root     string
	Engines  string // user-dropped Wine/GPTK builds
	Prefixes string // one Wine prefix per game or launcher
	Logs     string // one folder per game, one .log + .json per run
	Cache    string // downloaded installers
	DB       string // cave.json
}

// DefaultPaths returns ~/Library/Application Support/Cave, or $CAVE_HOME if set.
func DefaultPaths() (Paths, error) {
	root := os.Getenv("CAVE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, err
		}
		root = filepath.Join(home, "Library", "Application Support", "Cave")
	}
	return FromRoot(root), nil
}

func FromRoot(root string) Paths {
	return Paths{
		Root:     root,
		Engines:  filepath.Join(root, "engines"),
		Prefixes: filepath.Join(root, "prefixes"),
		Logs:     filepath.Join(root, "logs"),
		Cache:    filepath.Join(root, "cache"),
		DB:       filepath.Join(root, "cave.json"),
	}
}

func (p Paths) Ensure() error {
	for _, d := range []string{p.Root, p.Engines, p.Prefixes, p.Logs, p.Cache} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// Engine is a Wine build cave can launch with (GPTK, Wine Stable, ...).
type Engine struct {
	Name     string `json:"name"`
	Dir      string `json:"dir"`
	Wine     string `json:"wine"`
	D3DMetal bool   `json:"d3dmetal"`
	CX       bool   `json:"cx,omitempty"` // built from CrossOver sources: bundled dylibs, D3DMetal via CX_APPLEGPTK_*
}

// Prefix is an isolated Windows environment (a WINEPREFIX).
type Prefix struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Engine string `json:"engine"`
}

// Game is a launchable entry: an executable inside a prefix plus its profile.
type Game struct {
	Name    string            `json:"name"`
	Prefix  string            `json:"prefix"`
	Exe     string            `json:"exe"` // Windows path (C:\...) or Unix path
	Args    []string          `json:"args,omitempty"`
	WorkDir string            `json:"workdir,omitempty"`
	Backend string            `json:"backend"` // d3dmetal | dxvk | wined3d
	HUD     bool              `json:"hud,omitempty"`
	AVX     bool              `json:"avx,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type DB struct {
	DefaultEngine string            `json:"default_engine,omitempty"`
	Engines       map[string]Engine `json:"engines"`
	Prefixes      map[string]Prefix `json:"prefixes"`
	Games         map[string]Game   `json:"games"`
}

// Load reads the database; a missing file yields an empty one.
func Load(path string) (*DB, error) {
	db := &DB{}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, db); err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
	}
	if db.Engines == nil {
		db.Engines = map[string]Engine{}
	}
	if db.Prefixes == nil {
		db.Prefixes = map[string]Prefix{}
	}
	if db.Games == nil {
		db.Games = map[string]Game{}
	}
	return db, nil
}

// Save writes atomically (temp file + rename) so a crash never leaves a half-written DB.
func (db *DB) Save(path string) error {
	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Engine resolves a name, falling back to the default engine when name is empty.
func (db *DB) Engine(name string) (Engine, error) {
	if name == "" {
		name = db.DefaultEngine
	}
	if name == "" {
		return Engine{}, errors.New("no engine: run `cave engine detect --register` or `cave engine add <name> <dir>`")
	}
	e, ok := db.Engines[name]
	if !ok {
		return Engine{}, fmt.Errorf("unknown engine %q", name)
	}
	return e, nil
}

func (db *DB) Prefix(name string) (Prefix, error) {
	p, ok := db.Prefixes[name]
	if !ok {
		return Prefix{}, fmt.Errorf("unknown prefix %q (see `cave prefix list`)", name)
	}
	return p, nil
}

func (db *DB) Game(name string) (Game, error) {
	g, ok := db.Games[name]
	if !ok {
		return Game{}, fmt.Errorf("unknown game %q (see `cave list`)", name)
	}
	return g, nil
}

func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
