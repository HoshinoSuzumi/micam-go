package cli

import "testing"

// The version is injected at build time, so the only thing worth testing is the
// formatting: a plain `go build` says "dev" instead of pretending to be a release.
func TestFullVersion(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })

	cases := map[string]string{
		"0.6.2":            "micam-go 0.6.2",
		"0.6.2-3-gabc1234": "micam-go 0.6.2-3-gabc1234",
		"0.6.2-dirty":      "micam-go 0.6.2-dirty",
		"dev":              "micam-go dev",
	}
	for version, want := range cases {
		Version = version
		if got := FullVersion(); got != want {
			t.Errorf("FullVersion() with %q = %q, want %q", version, got, want)
		}
	}
}
