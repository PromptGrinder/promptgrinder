package discovery

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTechnologyDetectorAndRoleGenerator(t *testing.T) {
	tests := []struct {
		name         string
		files        map[string]string
		languages    []string
		technologies []string
		roles        []string
	}{
		{name: "spring boot maven with tests", files: map[string]string{
			"pom.xml":                `<dependency><artifactId>spring-boot-starter-web</artifactId></dependency>`,
			"src/main/java/App.java": "class App {}", "src/test/java/AppTest.java": "class AppTest {}",
		}, languages: []string{"Java"}, technologies: []string{"Spring Boot"}, roles: []string{"backend-feature", "backend-sonar", "backend-test"}},
		{name: "gradle quarkus", files: map[string]string{"service/build.gradle.kts": `plugins { id("io.quarkus") kotlin("jvm") }`}, languages: []string{"Kotlin"}, technologies: []string{"Quarkus"}, roles: []string{"backend-feature", "backend-sonar"}},
		{name: "android", files: map[string]string{"app/build.gradle.kts": `plugins { id("com.android.application") }`, "app/src/main/AndroidManifest.xml": `<manifest/>`}, languages: []string{"Kotlin"}, technologies: []string{"Android"}, roles: []string{"android-test", "android-ui"}},
		{name: "frontend variants", files: map[string]string{"web/package.json": `{"dependencies":{"next":"1","@angular/core":"1"},"devDependencies":{"vite":"1"}}`}, languages: []string{"JavaScript"}, technologies: []string{"Angular", "Frontend", "Next.js", "Vite"}, roles: []string{"frontend-feature"}},
		{name: "infrastructure", files: map[string]string{"Dockerfile": "FROM scratch", "compose.yaml": "services: {}", "infra/main.tf": "resource {}", "helm/app/Chart.yaml": "name: app", "k8s/app.yaml": "apiVersion: v1\nkind: Service\n"}, languages: []string{"HCL"}, technologies: []string{"Docker", "Docker Compose", "Helm", "Kubernetes", "Terraform"}, roles: []string{"infrastructure"}},
		{name: "ci", files: map[string]string{".github/workflows/test.yml": "name: test"}, technologies: []string{"GitHub Actions"}, roles: []string{"ci"}},
		{name: "documentation", files: map[string]string{"docs/guide.md": "# Guide"}, roles: []string{"documentation"}},
		{name: "mixed", files: map[string]string{"pom.xml": "spring-boot", "package.json": `{}`, ".github/workflows/ci.yaml": "name: ci", "README.md": "# Readme"}, languages: []string{"Java", "JavaScript"}, technologies: []string{"Frontend", "GitHub Actions", "Spring Boot"}, roles: []string{"backend-feature", "backend-sonar", "ci", "documentation", "frontend-feature"}},
		{name: "unsupported", files: map[string]string{"data/example.xyz": "unknown"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "repository with spaces")
			writeFixture(t, root, tc.files)
			d, err := (TechnologyDetector{}).Detect(root)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(d.Languages, nonNil(tc.languages)) {
				t.Errorf("languages = %#v, want %#v", d.Languages, nonNil(tc.languages))
			}
			if !reflect.DeepEqual(d.Technologies, nonNil(tc.technologies)) {
				t.Errorf("technologies = %#v, want %#v", d.Technologies, nonNil(tc.technologies))
			}
			roles := (RoleGenerator{}).Generate(d)
			ids := make([]string, len(roles))
			for i := range roles {
				ids[i] = roles[i].ID
			}
			if !reflect.DeepEqual(ids, nonNil(tc.roles)) {
				t.Errorf("roles = %#v, want %#v", ids, nonNil(tc.roles))
			}
			if !sort.StringsAreSorted(ids) {
				t.Errorf("roles are not sorted: %v", ids)
			}
		})
	}
}

func TestGeneratedContentIsDeterministicAndMinimal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "deterministic repo")
	writeFixture(t, root, map[string]string{"service/pom.xml": "spring-boot", "service/src/test/java/Test.java": "class Test {}"})
	first, err := Analyze(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Analyze(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Files, second.Files) {
		t.Fatal("identical repository produced different file content")
	}
	if len(first.Files) != 4 {
		t.Fatalf("files = %d, want manifest plus three roles", len(first.Files))
	}
	project := string(first.Files[0].Content)
	for _, want := range []string{"name: deterministic repo", "languages:\n  - Java", "frameworks:\n  - Spring Boot", "generated_by: promptgrinder discover"} {
		if !strings.Contains(project, want) {
			t.Errorf("project YAML missing %q:\n%s", want, project)
		}
	}
	role := string(first.Files[1].Content)
	for _, want := range []string{"allowed_paths:\n  - service", "preferred: local", "maven verify passes", "generated: true"} {
		if !strings.Contains(role, want) {
			t.Errorf("role YAML missing %q:\n%s", want, role)
		}
	}
	var manifest ProjectManifest
	if err := yaml.Unmarshal(first.Files[0].Content, &manifest); err != nil {
		t.Fatalf("parse project manifest: %v", err)
	}
	if manifest.Name == "" || len(manifest.Languages) == 0 || len(manifest.Frameworks) == 0 || len(manifest.Roles) == 0 || manifest.GeneratedBy != GeneratedBy {
		t.Fatalf("manifest public fields are incomplete: %+v", manifest)
	}
	var parsedRole Role
	if err := yaml.Unmarshal(first.Files[1].Content, &parsedRole); err != nil {
		t.Fatalf("parse role: %v", err)
	}
	if parsedRole.ID == "" || parsedRole.Name == "" || parsedRole.Description == "" || len(parsedRole.Technology) == 0 || len(parsedRole.AllowedPaths) == 0 || parsedRole.Runtime.Preferred == "" || len(parsedRole.QualityGates) == 0 || !parsedRole.Status.Generated {
		t.Fatalf("role public fields are incomplete: %+v", parsedRole)
	}
}

func TestDiscoverWithoutSupportedEvidenceCreatesEmptyProjectStructure(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"data/example.xyz": "unknown"})
	result, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Roles) != 0 || !reflect.DeepEqual(result.Files, []string{".promptgrinder/project.yaml"}) {
		t.Fatalf("result = %+v", result)
	}
	for _, dir := range []string{"roles", "context"} {
		if info, err := os.Stat(filepath.Join(root, ".promptgrinder", dir)); err != nil || !info.IsDir() {
			t.Fatalf("%s directory: %v", dir, err)
		}
	}
}

func TestDiscoverWritesOnlyPromptGrinderAndLeavesIdenticalFilesUntouched(t *testing.T) {
	root := filepath.Join(t.TempDir(), "safe repo with spaces")
	writeFixture(t, root, map[string]string{"pom.xml": "spring-boot", "keep.txt": "unchanged"})
	result, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 3 {
		t.Fatalf("files = %v", result.Files)
	}
	if info, err := os.Stat(filepath.Join(root, ".promptgrinder", "context")); err != nil || !info.IsDir() {
		t.Fatalf("context directory: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(root, ".promptgrinder", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(root); err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(root, ".promptgrinder", "project.yaml"))
	if !bytes.Equal(before, after) {
		t.Fatal("existing project file was changed")
	}
	keep, _ := os.ReadFile(filepath.Join(root, "keep.txt"))
	if string(keep) != "unchanged" {
		t.Fatal("file outside .promptgrinder changed")
	}
}

func TestWritePlanPreflightsAllConflictsWithoutPartialWrites(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{".promptgrinder/roles/backend-feature.yaml": "owned by user"})
	plan := Plan{Files: []File{{Path: ".promptgrinder/project.yaml", Content: []byte("name: x\n")}, {Path: ".promptgrinder/roles/backend-feature.yaml", Content: []byte("id: backend-feature\n")}}}
	if err := WritePlan(root, plan); err == nil {
		t.Fatal("expected overwrite conflict")
	}
	if _, err := os.Stat(filepath.Join(root, ".promptgrinder", "project.yaml")); !os.IsNotExist(err) {
		t.Fatalf("project file was partially written: %v", err)
	}
}

func TestExistingEmptyContextDirectoryIsAllowed(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"pom.xml": "spring-boot"})
	if err := os.MkdirAll(filepath.Join(root, ".promptgrinder", "context"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(root); err != nil {
		t.Fatal(err)
	}
}

func TestWritePlanRejectsPathsOutsidePromptGrinder(t *testing.T) {
	for _, path := range []string{"outside.yaml", "../outside.yaml", ".promptgrinder-other/file"} {
		t.Run(path, func(t *testing.T) {
			if err := WritePlan(t.TempDir(), Plan{Files: []File{{Path: path}}}); err == nil {
				t.Fatal("expected unsafe path error")
			}
		})
	}
}

func writeFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
