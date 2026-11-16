package llmsemantic_test

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestModulePathCarriesTheReleaseMajor defends EST-6612: Go ignores a v2+ tag
// on a module whose path has no /vN suffix, so after tagging v2.0.0 a
// `go install .../cmd/llm-semantic@latest` still installed v1.2.0, the release
// with the plain-text default. The newest CHANGELOG release sets the major the
// module path must carry, and every README `go install` line must use it.
func TestModulePathCarriesTheReleaseMajor(t *testing.T) {
	changelog, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	release := regexp.MustCompile(`(?m)^## \[(\d+)\.\d+\.\d+\]`).FindSubmatch(changelog)
	if release == nil {
		t.Fatal("CHANGELOG.md has no released version heading")
	}
	major, _ := strconv.Atoi(string(release[1]))

	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	module := regexp.MustCompile(`(?m)^module\s+(\S+)`).FindSubmatch(gomod)
	if module == nil {
		t.Fatal("go.mod has no module line")
	}
	path := string(module[1])

	want := "github.com/samestrin/llm-semantic-axi"
	if major >= 2 {
		want += fmt.Sprintf("/v%d", major)
	}
	if path != want {
		t.Fatalf("go.mod module is %q; CHANGELOG's newest release is %d.x, so it must be %q", path, major, want)
	}

	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	installs := regexp.MustCompile(`go install (github\.com/\S+)`).FindAllSubmatch(readme, -1)
	if len(installs) == 0 {
		t.Fatal("README.md has no `go install` line")
	}
	for _, m := range installs {
		if target := string(m[1]); !strings.HasPrefix(target, want+"/cmd/") {
			t.Errorf("README installs %q, outside module %q", target, want)
		}
	}
}
