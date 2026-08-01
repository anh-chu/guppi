package common

// NAME of the App
var NAME = "termyard"

// SUMMARY of the Version
var SUMMARY = "v4.2.2" // x-release-please-version

// BRANCH of the Version
var BRANCH = "dev"

// VERSION of Release
var VERSION = "4.2.2" // x-release-please-version

var COMMIT = "dirty"

// AppVersion --
var AppVersion AppVersionInfo

// AppVersionInfo --
type AppVersionInfo struct {
	Name    string
	Version string
	Branch  string
	Summary string
	Commit  string
}

func init() {
	AppVersion = AppVersionInfo{
		Name:    NAME,
		Version: VERSION,
		Branch:  BRANCH,
		Summary: SUMMARY,
		Commit:  COMMIT,
	}
}
