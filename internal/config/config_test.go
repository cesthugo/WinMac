package config

import (
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cave.json")
	db, err := Load(path) // missing file -> empty DB
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Engine(""); err == nil {
		t.Error("empty DB should have no default engine")
	}
	db.Engines["gptk"] = Engine{Name: "gptk", Wine: "/e/wine64", D3DMetal: true}
	db.DefaultEngine = "gptk"
	db.Games["sm2"] = Game{Name: "sm2", Prefix: "steam", Backend: "d3dmetal", AVX: true, Env: map[string]string{"A": "1"}}
	if err := db.Save(path); err != nil {
		t.Fatal(err)
	}
	db2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if e, err := db2.Engine(""); err != nil || !e.D3DMetal {
		t.Errorf("default engine lost: %+v %v", e, err)
	}
	if g, err := db2.Game("sm2"); err != nil || !g.AVX || g.Env["A"] != "1" {
		t.Errorf("game lost: %+v %v", g, err)
	}
	if _, err := db2.Prefix("nope"); err == nil {
		t.Error("unknown prefix should fail")
	}
}
