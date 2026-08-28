package app

import (
	"os"
	"testing"

	"github.com/zJay26/codex-usage/internal/install"
)

func TestBindCurrentCandidateImageMatchesRunningExecutable(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path, err = canonicalAbsolutePath(path)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := install.FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	image, err := bindCurrentCandidateImage(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = image.Close() })
	gotDigest, err := image.digestForPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotDigest != wantDigest {
		t.Fatalf("bound digest=%q want running executable digest=%q", gotDigest, wantDigest)
	}
}
