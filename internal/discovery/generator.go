package discovery

import (
	"path/filepath"
	"sort"
	"strings"
)

type RoleGenerator struct{}

func (RoleGenerator) Generate(d Detection) []Role {
	var roles []Role
	add := func(id, name, description string, tech, paths, gates []string) {
		roles = append(roles, Role{ID: id, Name: name, Description: description, Technology: sortedUnique(tech), AllowedPaths: sortedUnique(paths), Runtime: Runtime{Preferred: "local"}, QualityGates: sortedUnique(gates), Status: Status{Generated: true}})
	}
	backendTech := intersection(d.Technologies, []string{"Micronaut", "Quarkus", "Spring Boot"})
	buildGates := buildQualityGates(d.BuildTools)
	if len(d.BackendRoots) > 0 {
		add("backend-feature", "Backend Feature", "Implement backend features within detected backend modules.", backendTech, subtreePatterns(d.BackendRoots), buildGates)
		add("backend-sonar", "Backend Sonar", "Maintain backend static-analysis quality within detected backend modules.", backendTech, subtreePatterns(d.BackendRoots), append(buildGates, "static analysis passes"))
	}
	if len(d.BackendTests) > 0 && len(d.BackendRoots) > 0 {
		add("backend-test", "Backend Test", "Add and maintain backend tests in detected test source sets.", backendTech, subtreePatterns(d.BackendTests), append(buildGates, "tests pass"))
	}
	if len(d.AndroidRoots) > 0 {
		add("android-ui", "Android UI", "Implement Android user-interface changes in detected Android modules.", []string{"Android"}, subtreePatterns(d.AndroidRoots), append(buildGates, "Android build passes"))
		add("android-test", "Android Test", "Add and maintain tests for detected Android modules.", []string{"Android"}, subtreePatterns(d.AndroidRoots), append(buildGates, "Android tests pass"))
	}
	if len(d.FrontendRoots) > 0 {
		add("frontend-feature", "Frontend Feature", "Implement frontend features in detected frontend projects.", intersection(d.Technologies, []string{"Angular", "Frontend", "Next.js", "Vite"}), subtreePatterns(d.FrontendRoots), []string{"frontend build passes", "frontend tests pass"})
	}
	if len(d.InfraPaths) > 0 {
		add("infrastructure", "Infrastructure", "Maintain directly detected infrastructure configuration.", intersection(d.Technologies, []string{"Docker", "Docker Compose", "Helm", "Kubernetes", "Terraform"}), d.InfraPaths, []string{"configuration validates"})
	}
	if len(d.CIPaths) > 0 {
		add("ci", "Continuous Integration", "Maintain detected GitHub Actions workflows.", []string{"GitHub Actions"}, d.CIPaths, []string{"workflow syntax validates"})
	}
	if len(d.DocPaths) > 0 {
		add("documentation", "Documentation", "Maintain repository documentation in detected documentation paths.", []string{"Markdown"}, subtreePatterns(d.DocPaths), []string{"documentation links validate"})
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].ID < roles[j].ID })
	return roles
}

func subtreePatterns(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "." {
			out = append(out, "**")
		} else if base := filepath.Base(value); filepath.Ext(base) != "" && !strings.HasPrefix(base, ".") {
			out = append(out, value)
		} else {
			out = append(out, strings.TrimSuffix(value, "/")+"/**")
		}
	}
	return out
}

func buildQualityGates(tools []string) []string {
	out := []string{}
	for _, t := range tools {
		switch t {
		case "Maven":
			out = append(out, "maven verify passes")
		case "Gradle":
			out = append(out, "gradle check passes")
		}
	}
	return out
}
func intersection(values, allowed []string) []string {
	a := set{}
	for _, v := range allowed {
		a.add(v)
	}
	out := []string{}
	for _, v := range values {
		if _, ok := a[v]; ok {
			out = append(out, v)
		}
	}
	return out
}
func sortedUnique(values []string) []string {
	s := set{}
	for _, v := range values {
		s.add(v)
	}
	return s.sorted()
}
