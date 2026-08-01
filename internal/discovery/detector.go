package discovery

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type TechnologyDetector struct{}

func (TechnologyDetector) Detect(root string) (Detection, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Detection{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Detection{}, err
	}
	if !info.IsDir() {
		return Detection{}, fmt.Errorf("repository root %q is not a directory", root)
	}

	d := Detection{RepositoryName: filepath.Base(abs)}
	languages, technologies, tools := set{}, set{}, set{}
	backendRoots, backendTests, androidRoots := set{}, set{}, set{}
	frontendRoots, infraPaths, ciPaths, docPaths, contextPaths := set{}, set{}, set{}, set{}, set{}

	err = filepath.WalkDir(abs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel != "." && (entry.Name() == ".git" || entry.Name() == ".promptgrinder" || entry.Name() == "node_modules" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			if rel == "docs" || strings.HasPrefix(rel, "docs/") {
				docPaths.add(topOrSelf(rel, "docs"))
			}
			if isContextDir(entry.Name()) {
				contextPaths.add(rel)
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		name, dir := entry.Name(), filepath.ToSlash(filepath.Dir(rel))
		if dir == "." {
			dir = "."
		}
		lower := strings.ToLower(name)
		switch {
		case lower == "go.mod":
			languages.add("Go")
			tools.add("Go modules")
		case lower == "pom.xml":
			languages.add("Java")
			tools.add("Maven")
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			framework := jvmFramework(string(content))
			if framework != "" {
				technologies.add(framework)
				backendRoots.add(dir)
			}
		case lower == "build.gradle" || lower == "build.gradle.kts":
			tools.add("Gradle")
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(content)
			if strings.Contains(lower, ".kts") || strings.Contains(text, "kotlin(") || strings.Contains(text, "org.jetbrains.kotlin") {
				languages.add("Kotlin")
			} else {
				languages.add("Java")
			}
			if strings.Contains(text, "com.android.application") || strings.Contains(text, "com.android.library") {
				technologies.add("Android")
				androidRoots.add(dir)
			}
			if containsComposeMarker(text) {
				technologies.add("Jetpack Compose")
			}
			if framework := jvmFramework(text); framework != "" {
				technologies.add(framework)
				backendRoots.add(dir)
			}
		case lower == "androidmanifest.xml":
			technologies.add("Android")
			androidRoots.add(androidProjectRoot(rel))
		case lower == "package.json":
			languages.add("JavaScript")
			technologies.add("Frontend")
			frontendRoots.add(dir)
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, tech := range packageTechnologies(content) {
				technologies.add(tech)
			}
		case lower == "angular.json":
			technologies.add("Angular")
			technologies.add("Frontend")
			languages.add("TypeScript")
			frontendRoots.add(dir)
		case lower == "vite.config.js" || lower == "vite.config.ts" || lower == "vite.config.mjs":
			technologies.add("Vite")
			technologies.add("Frontend")
			frontendRoots.add(dir)
		case strings.HasPrefix(lower, "next.config."):
			technologies.add("Next.js")
			technologies.add("Frontend")
			frontendRoots.add(dir)
		case lower == "dockerfile" || strings.HasPrefix(lower, "dockerfile."):
			technologies.add("Docker")
			infraPaths.add(rel)
		case lower == "docker-compose.yml" || lower == "docker-compose.yaml" || lower == "compose.yml" || lower == "compose.yaml":
			technologies.add("Docker Compose")
			infraPaths.add(rel)
		case strings.HasSuffix(lower, ".tf"):
			technologies.add("Terraform")
			languages.add("HCL")
			infraPaths.add(rel)
		case lower == "chart.yaml" && (dir == "helm" || strings.HasPrefix(dir, "helm/") || strings.Contains(dir, "/helm/") || strings.Contains(dir, "chart")):
			technologies.add("Helm")
			infraPaths.add(dir)
		case (strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")) && looksLikeKubernetes(path):
			technologies.add("Kubernetes")
			infraPaths.add(rel)
		}
		if strings.HasPrefix(rel, ".github/workflows/") && (strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")) {
			technologies.add("GitHub Actions")
			ciPaths.add(rel)
		}
		if lower == "readme.md" || lower == "readme" || strings.HasPrefix(rel, "docs/") {
			docPaths.add(ifRootFile(rel))
		}
		if isBackendTest(rel) {
			backendTests.add(testRoot(rel))
		}
		return nil
	})
	if err != nil {
		return Detection{}, fmt.Errorf("scan repository: %w", err)
	}
	d.Languages, d.Technologies, d.BuildTools = languages.sorted(), technologies.sorted(), tools.sorted()
	d.BackendRoots, d.AndroidRoots = backendRoots.sorted(), androidRoots.sorted()
	d.BackendTests = testsWithinRoots(backendTests.sorted(), d.BackendRoots)
	d.FrontendRoots, d.InfraPaths, d.CIPaths = frontendRoots.sorted(), infraPaths.sorted(), ciPaths.sorted()
	d.DocPaths, d.ContextPaths = docPaths.sorted(), contextPaths.sorted()
	return d, nil
}

type set map[string]struct{}

func (s set) add(v string) {
	if v != "" {
		s[v] = struct{}{}
	}
}
func (s set) sorted() []string {
	out := make([]string, 0, len(s))
	for v := range s {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func jvmFramework(s string) string {
	lower := strings.ToLower(s)
	for _, x := range []struct{ marker, name string }{{"spring-boot", "Spring Boot"}, {"io.quarkus", "Quarkus"}, {"micronaut", "Micronaut"}} {
		if strings.Contains(lower, x.marker) {
			return x.name
		}
	}
	return ""
}
func packageTechnologies(b []byte) []string {
	var p struct {
		Dependencies    map[string]json.RawMessage `json:"dependencies"`
		DevDependencies map[string]json.RawMessage `json:"devDependencies"`
	}
	if json.Unmarshal(b, &p) != nil {
		return nil
	}
	all := set{}
	for k := range p.Dependencies {
		all.add(k)
	}
	for k := range p.DevDependencies {
		all.add(k)
	}
	out := []string{}
	for _, x := range []struct{ k, n string }{{"@angular/core", "Angular"}, {"vite", "Vite"}, {"next", "Next.js"}} {
		if _, ok := all[x.k]; ok {
			out = append(out, x.n)
		}
	}
	return out
}

func containsComposeMarker(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range []string{"androidx.compose", "org.jetbrains.compose", "compose = true", "compose=true"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
func looksLikeKubernetes(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "apiVersion:") && strings.Contains(s, "kind:")
}
func isContextDir(n string) bool {
	n = strings.ToLower(n)
	return n == "skills" || n == "context" || n == ".agents" || n == ".codex"
}
func topOrSelf(rel, top string) string {
	if rel == top || strings.HasPrefix(rel, top+"/") {
		return top
	}
	return rel
}
func ifRootFile(rel string) string {
	if !strings.Contains(rel, "/") {
		return rel
	}
	return strings.Split(rel, "/")[0]
}
func androidProjectRoot(rel string) string {
	const marker = "/src/main/AndroidManifest.xml"
	if i := strings.Index(rel, marker); i >= 0 {
		return rel[:i]
	}
	return filepath.ToSlash(filepath.Dir(rel))
}
func isBackendTest(rel string) bool {
	return strings.Contains(rel, "/src/test/") || strings.HasPrefix(rel, "src/test/")
}
func testRoot(rel string) string {
	if i := strings.Index(rel, "src/test/"); i >= 0 {
		return strings.TrimSuffix(rel[:i]+"src/test", "/")
	}
	return filepath.ToSlash(filepath.Dir(rel))
}

func testsWithinRoots(tests, roots []string) []string {
	var out []string
	for _, test := range tests {
		for _, root := range roots {
			if root == "." || test == root || strings.HasPrefix(test, root+"/") {
				out = append(out, test)
				break
			}
		}
	}
	return out
}
