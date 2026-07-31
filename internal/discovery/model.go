package discovery

// Detection is the repository evidence used to generate a project manifest and roles.
type Detection struct {
	RepositoryName string
	Languages      []string
	Technologies   []string
	BackendRoots   []string
	BackendTests   []string
	AndroidRoots   []string
	FrontendRoots  []string
	InfraPaths     []string
	CIPaths        []string
	DocPaths       []string
	ContextPaths   []string
	BuildTools     []string
}

type Runtime struct {
	Preferred string `yaml:"preferred"`
}

type Status struct {
	Generated bool `yaml:"generated"`
}

type Role struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Technology   []string `yaml:"technology"`
	AllowedPaths []string `yaml:"allowed_paths"`
	Runtime      Runtime  `yaml:"runtime"`
	QualityGates []string `yaml:"quality_gates"`
	Status       Status   `yaml:"status"`
}

type ProjectManifest struct {
	Name        string   `yaml:"name"`
	Languages   []string `yaml:"languages"`
	Frameworks  []string `yaml:"frameworks"`
	Roles       []string `yaml:"roles"`
	GeneratedBy string   `yaml:"generated_by"`
}

type File struct {
	Path    string
	Content []byte
}

type Plan struct {
	Detection Detection
	Manifest  ProjectManifest
	Roles     []Role
	Files     []File
}

type Result struct {
	Detection Detection
	Roles     []Role
	Files     []string
}
