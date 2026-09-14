package utility

import (
	"reflect"
	"testing"
)

func TestGitCommandArgsTrustOnlyExactWorkspace(t *testing.T) {
	got := gitCommandArgs("/workspace", "fetch", "--prune", "origin")
	want := []string{"-c", "safe.directory=/workspace", "fetch", "--prune", "origin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("git arguments = %#v, want %#v", got, want)
	}
}
