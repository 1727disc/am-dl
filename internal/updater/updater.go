package updater

// RepoOwner and RepoName default to this fork and can be set at build time.
var (
	RepoOwner = "1727disc"
	RepoName  = "am-dl"
)

// DefaultConfigExample holds the embedded content of config.yaml.example,
// populated at runtime from package main via init().
var DefaultConfigExample string
