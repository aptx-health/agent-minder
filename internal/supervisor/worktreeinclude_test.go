package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestCopyWorktreeIncludesNoIncludeFileIsNoop(t *testing.T) {
	repo := initWorktreeIncludeRepo(t)
	dst := t.TempDir()
	writeFile(t, filepath.Join(repo, ".env"), "secret", 0o600)

	result, err := copyWorktreeIncludes(repo, dst)
	if err != nil {
		t.Fatalf("copyWorktreeIncludes: %v", err)
	}
	if len(result.Copied) != 0 || len(result.Warnings) != 0 {
		t.Fatalf("result = %+v, want empty", result)
	}
	if _, err := os.Stat(filepath.Join(dst, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env copied without include file; stat err=%v", err)
	}
}

func TestCopyWorktreeIncludesCopiesPatternsNegationAndDirectories(t *testing.T) {
	repo := initWorktreeIncludeRepo(t)
	writeInclude(t, repo, strings.Join([]string{
		"# local runtime files",
		".env*",
		"!.env.example",
		"certs/",
	}, "\n"))
	writeFile(t, filepath.Join(repo, ".env"), "secret", 0o600)
	writeFile(t, filepath.Join(repo, ".env.example"), "example", 0o644)
	writeFile(t, filepath.Join(repo, "certs", "local.pem"), "pem", 0o600)
	writeFile(t, filepath.Join(repo, "certs", "nested", "chain.pem"), "chain", 0o600)

	dst := t.TempDir()
	result, err := copyWorktreeIncludes(repo, dst)
	if err != nil {
		t.Fatalf("copyWorktreeIncludes: %v", err)
	}

	wantCopied := []string{".env", "certs/local.pem", "certs/nested/chain.pem"}
	if !slices.Equal(result.Copied, wantCopied) {
		t.Fatalf("copied = %#v, want %#v", result.Copied, wantCopied)
	}
	assertFileContent(t, filepath.Join(dst, ".env"), "secret")
	assertFileContent(t, filepath.Join(dst, "certs", "local.pem"), "pem")
	assertFileContent(t, filepath.Join(dst, "certs", "nested", "chain.pem"), "chain")
	if _, err := os.Stat(filepath.Join(dst, ".env.example")); !os.IsNotExist(err) {
		t.Fatalf(".env.example should be excluded by negation; stat err=%v", err)
	}
}

func TestCopyWorktreeIncludesSkipsTrackedExistingAndSymlink(t *testing.T) {
	repo := initWorktreeIncludeRepo(t)
	writeInclude(t, repo, strings.Join([]string{
		"tracked.txt",
		"local.txt",
		"link.txt",
	}, "\n"))
	writeFile(t, filepath.Join(repo, "tracked.txt"), "tracked source", 0o644)
	runGit(t, repo, "add", "tracked.txt")
	writeFile(t, filepath.Join(repo, "local.txt"), "local source", 0o644)
	if err := os.Symlink("local.txt", filepath.Join(repo, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dst := t.TempDir()
	writeFile(t, filepath.Join(dst, "local.txt"), "existing dest", 0o644)

	result, err := copyWorktreeIncludes(repo, dst)
	if err != nil {
		t.Fatalf("copyWorktreeIncludes: %v", err)
	}
	if len(result.Copied) != 0 {
		t.Fatalf("copied = %#v, want none", result.Copied)
	}
	assertFileContent(t, filepath.Join(dst, "local.txt"), "existing dest")
	assertWarningsContain(t, result.Warnings, "tracked.txt", "matched no untracked files")
	assertWarningsContain(t, result.Warnings, "local.txt", "destination already exists")
	assertWarningsContain(t, result.Warnings, "link.txt", "skipped symlink")
}

func TestCopyWorktreeIncludesWarnsWithoutFailingForMissingAndUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadable files are not portable on windows")
	}
	repo := initWorktreeIncludeRepo(t)
	writeInclude(t, repo, strings.Join([]string{
		"missing.env",
		"private.key",
	}, "\n"))
	privateKey := filepath.Join(repo, "private.key")
	writeFile(t, privateKey, "key", 0o600)
	if err := os.Chmod(privateKey, 0); err != nil {
		t.Fatalf("chmod private.key: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(privateKey, 0o600) })

	result, err := copyWorktreeIncludes(repo, t.TempDir())
	if err != nil {
		t.Fatalf("copyWorktreeIncludes: %v", err)
	}
	if len(result.Copied) != 0 {
		t.Fatalf("copied = %#v, want none", result.Copied)
	}
	assertWarningsContain(t, result.Warnings, "missing.env", "matched no untracked files")
	assertWarningsContain(t, result.Warnings, "private.key", "open source")
}

func initWorktreeIncludeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	return repo
}

func writeInclude(t *testing.T, repo, contents string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(worktreeIncludePath))
	writeFile(t, path, contents, 0o644)
	runGit(t, repo, "add", worktreeIncludePath)
}

func writeFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertWarningsContain(t *testing.T, warnings []string, parts ...string) {
	t.Helper()
	for _, warning := range warnings {
		matches := true
		for _, part := range parts {
			if !strings.Contains(warning, part) {
				matches = false
				break
			}
		}
		if matches {
			return
		}
	}
	t.Fatalf("warnings %#v do not contain parts %#v", warnings, parts)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
