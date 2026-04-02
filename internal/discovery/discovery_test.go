package discovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// initGitRepo initialises a bare-minimum git repo in dir so that
// gitpkg.IsRepo returns true.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	// git init + an empty commit so HEAD is valid.
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	} {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
}

func TestScanRepo_FullInventory(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Create language markers.
	touch(t, dir, "go.mod")
	touch(t, dir, "package.json")
	touch(t, dir, "Cargo.toml")
	touch(t, dir, "pyproject.toml")
	touch(t, dir, "Gemfile")
	touch(t, dir, "pom.xml")

	// Create CI markers.
	mkdirAll(t, dir, ".github/workflows")
	mkdirAll(t, dir, ".circleci")
	touch(t, dir, ".gitlab-ci.yml")
	touch(t, dir, "Jenkinsfile")

	// Create build files.
	touch(t, dir, "Makefile")
	touch(t, dir, "Dockerfile")
	touch(t, dir, "docker-compose.yml")
	touch(t, dir, "Taskfile.yml")

	info, err := ScanRepo(dir)
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}

	// Languages (order is non-deterministic due to map iteration).
	wantLangs := []string{"go", "javascript", "rust", "python", "ruby", "java"}
	for _, lang := range wantLangs {
		if !slices.Contains(info.Inventory.Languages, lang) {
			t.Errorf("missing language %q in %v", lang, info.Inventory.Languages)
		}
	}
	if len(info.Inventory.Languages) != len(wantLangs) {
		t.Errorf("expected %d languages, got %d: %v", len(wantLangs), len(info.Inventory.Languages), info.Inventory.Languages)
	}

	// CI systems.
	wantCI := []string{"GitHub Actions", "CircleCI", "GitLab CI", "Jenkins"}
	for _, ci := range wantCI {
		if !slices.Contains(info.Inventory.CI, ci) {
			t.Errorf("missing CI %q in %v", ci, info.Inventory.CI)
		}
	}
	if len(info.Inventory.CI) != len(wantCI) {
		t.Errorf("expected %d CI entries, got %d: %v", len(wantCI), len(info.Inventory.CI), info.Inventory.CI)
	}

	// Build files.
	wantBuild := []string{"Makefile", "Dockerfile", "docker-compose.yml", "Taskfile.yml"}
	for _, bf := range wantBuild {
		if !slices.Contains(info.Inventory.BuildFiles, bf) {
			t.Errorf("missing build file %q in %v", bf, info.Inventory.BuildFiles)
		}
	}
	if len(info.Inventory.BuildFiles) != len(wantBuild) {
		t.Errorf("expected %d build files, got %d: %v", len(wantBuild), len(info.Inventory.BuildFiles), info.Inventory.BuildFiles)
	}
}

func TestScanRepo_EmptyRepo(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	info, err := ScanRepo(dir)
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}

	if len(info.Inventory.Languages) != 0 {
		t.Errorf("expected no languages, got %v", info.Inventory.Languages)
	}
	if len(info.Inventory.CI) != 0 {
		t.Errorf("expected no CI, got %v", info.Inventory.CI)
	}
	if len(info.Inventory.BuildFiles) != 0 {
		t.Errorf("expected no build files, got %v", info.Inventory.BuildFiles)
	}
}

func TestScanRepo_NotARepo(t *testing.T) {
	dir := t.TempDir()

	_, err := ScanRepo(dir)
	if err == nil {
		t.Fatal("expected error for non-repo directory")
	}
	narErr, ok := err.(*NotARepoError)
	if !ok {
		t.Fatalf("expected *NotARepoError, got %T: %v", err, err)
	}
	if narErr.Path == "" {
		t.Error("NotARepoError.Path should not be empty")
	}
}

func TestScanRepo_DuplicateLanguage(t *testing.T) {
	// Both pyproject.toml and setup.py map to "python"; ensure no duplicates.
	dir := t.TempDir()
	initGitRepo(t, dir)

	touch(t, dir, "pyproject.toml")
	touch(t, dir, "setup.py")

	info, err := ScanRepo(dir)
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}

	count := 0
	for _, lang := range info.Inventory.Languages {
		if lang == "python" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected python once, got %d times in %v", count, info.Inventory.Languages)
	}
}

func TestScanRepo_PartialInventory(t *testing.T) {
	// Only some markers present.
	dir := t.TempDir()
	initGitRepo(t, dir)

	touch(t, dir, "go.mod")
	touch(t, dir, "Dockerfile")

	info, err := ScanRepo(dir)
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}

	if len(info.Inventory.Languages) != 1 || info.Inventory.Languages[0] != "go" {
		t.Errorf("expected [go], got %v", info.Inventory.Languages)
	}
	if len(info.Inventory.CI) != 0 {
		t.Errorf("expected no CI, got %v", info.Inventory.CI)
	}
	if len(info.Inventory.BuildFiles) != 1 || info.Inventory.BuildFiles[0] != "Dockerfile" {
		t.Errorf("expected [Dockerfile], got %v", info.Inventory.BuildFiles)
	}
}

func TestScanRepo_JavaDuplicateDetection(t *testing.T) {
	// Both pom.xml and build.gradle map to "java"; ensure no duplicates.
	dir := t.TempDir()
	initGitRepo(t, dir)

	touch(t, dir, "pom.xml")
	touch(t, dir, "build.gradle")

	info, err := ScanRepo(dir)
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}

	count := 0
	for _, lang := range info.Inventory.Languages {
		if lang == "java" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected java once, got %d times in %v", count, info.Inventory.Languages)
	}
}

// --- helpers ---

func touch(t *testing.T, dir, name string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatalf("touch %s: %v", name, err)
	}
}

func mkdirAll(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
}
