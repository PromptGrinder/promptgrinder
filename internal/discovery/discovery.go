package discovery

const GeneratedBy = "promptgrinder discover"

func Analyze(root string) (Plan, error) {
	d, err := (TechnologyDetector{}).Detect(root)
	if err != nil {
		return Plan{}, err
	}
	roles := (RoleGenerator{}).Generate(d)
	ids := make([]string, len(roles))
	for i, r := range roles {
		ids[i] = r.ID
	}
	manifest := ProjectManifest{Name: d.RepositoryName, Languages: d.Languages, Frameworks: d.Technologies, Roles: ids, GeneratedBy: GeneratedBy}
	files, err := (ProjectManifestWriter{}).Files(manifest, roles)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Detection: d, Manifest: manifest, Roles: roles, Files: files}, nil
}

func Discover(root string) (Result, error) {
	plan, err := Analyze(root)
	if err != nil {
		return Result{}, err
	}
	if err := WritePlan(root, plan); err != nil {
		return Result{}, err
	}
	paths := make([]string, len(plan.Files))
	for i, f := range plan.Files {
		paths[i] = f.Path
	}
	return Result{Detection: plan.Detection, Roles: plan.Roles, Files: paths}, nil
}
