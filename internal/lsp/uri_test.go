package lsp

import (
	"net/url"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPathToURI(t *testing.T) {
	if runtime.GOOS == "windows" {
		got := PathToURI(`D:\CODE\ai\x\main.go`)
		if got != "file:///D:/CODE/ai/x/main.go" {
			t.Fatalf("PathToURI windows = %q", got)
		}
		return
	}
	got := PathToURI("/home/u/x/main.go")
	if got != "file:///home/u/x/main.go" {
		t.Fatalf("PathToURI posix = %q", got)
	}
}

func TestPathToURISpaces(t *testing.T) {
	got := PathToURI(filepath.Join(t.TempDir(), "a b.go"))
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	if u.Scheme != "file" {
		t.Fatalf("scheme = %q", u.Scheme)
	}
}

func TestURIToPath(t *testing.T) {
	cases := []struct {
		uri, want string
	}{
		// Windows drive forms (plain and escaped colon, both emitted in the wild).
		{"file:///D:/CODE/ai/x/main.go", `D:\CODE\ai\x\main.go`},
		{"file:///D%3A/CODE/ai/x/main.go", `D:\CODE\ai\x\main.go`},
		{"file:///d%3A/code/x.go", `d:\code\x.go`},
		// POSIX.
		{"file:///home/u/x/main.go", `/home/u/x/main.go`},
		// A POSIX path that merely contains a colon must stay POSIX.
		{"file:///a:b/c.go", `/a:b/c.go`},
	}
	if runtime.GOOS != "windows" {
		// On POSIX the drive forms decode to the literal slash path.
		cases[0].want = "/D:/CODE/ai/x/main.go"
		cases[1].want = "/D:/CODE/ai/x/main.go"
		cases[2].want = "/d:/code/x.go"
	}
	for _, tc := range cases {
		if got := URIToPath(tc.uri); got != tc.want {
			t.Errorf("URIToPath(%q) = %q, want %q", tc.uri, got, tc.want)
		}
	}
}

func TestURIRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "roundtrip.go")
	if got := URIToPath(PathToURI(p)); got != p {
		t.Fatalf("round trip: %q → %q", p, got)
	}
}

func TestPathToURIRelativeResolves(t *testing.T) {
	got := PathToURI("x.go")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Scheme != "file" {
		t.Fatalf("relative path did not resolve: %q", got)
	}
}
