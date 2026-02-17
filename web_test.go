package tokenhub

import (
	"embed"
	"strings"
	"testing"
)

func TestWebFSNotNil(t *testing.T) {
	// embed.FS is a value type; a zero-value FS contains no files.
	// Verify WebFS is populated by checking it differs from the zero value.
	var zero embed.FS
	if _, err := zero.ReadDir("."); err == nil {
		// zero value ReadDir(".") returns nil, nil — compare entries instead.
	}
	entries, err := WebFS.ReadDir("web")
	if err != nil {
		t.Fatalf("WebFS.ReadDir(\"web\") failed: %v — embedded FS appears uninitialized", err)
	}
	if len(entries) == 0 {
		t.Fatal("WebFS contains no entries under web/")
	}
}

func TestWebFSIndexHTMLExists(t *testing.T) {
	data, err := WebFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("expected web/index.html in embedded FS: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("web/index.html is empty")
	}
}

func TestWebFSCytoscapeExists(t *testing.T) {
	data, err := WebFS.ReadFile("web/cytoscape.min.js")
	if err != nil {
		t.Fatalf("expected web/cytoscape.min.js in embedded FS: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("web/cytoscape.min.js is empty")
	}
}

func TestWebFSD3Exists(t *testing.T) {
	data, err := WebFS.ReadFile("web/d3.min.js")
	if err != nil {
		t.Fatalf("expected web/d3.min.js in embedded FS: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("web/d3.min.js is empty")
	}
}

func TestWebFSIndexHTMLContent(t *testing.T) {
	data, err := WebFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("failed to read web/index.html: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "<!DOCTYPE html>") {
		t.Error("web/index.html missing <!DOCTYPE html> declaration")
	}
	if !strings.Contains(content, "TokenHub") {
		t.Error("web/index.html missing expected 'TokenHub' text")
	}
}
