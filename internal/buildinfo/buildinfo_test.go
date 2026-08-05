package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestFormatIncludesShortRevisionAndDirtyState(t *testing.T) {
	settings := []debug.BuildSetting{
		{Key: "vcs.revision", Value: "5716a42123456789"},
		{Key: "vcs.modified", Value: "true"},
	}
	if got, want := format("v1.0.0-rc.2.4", settings), "v1.0.0-rc.2.4 (5716a42, dirty)"; got != want {
		t.Fatalf("format() = %q, want %q", got, want)
	}
}

func TestFormatWithoutVCSMetadataReturnsVersion(t *testing.T) {
	if got, want := format("v1.2.3", nil), "v1.2.3"; got != want {
		t.Fatalf("format() = %q, want %q", got, want)
	}
}

func TestStringPrefersInjectedRevision(t *testing.T) {
	oldVersion, oldRevision := Version, Revision
	t.Cleanup(func() {
		Version, Revision = oldVersion, oldRevision
	})
	Version = "v1.0.0-rc.2.4"
	Revision = "5716a42123456789"
	if got, want := String(), "v1.0.0-rc.2.4 (5716a42)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
