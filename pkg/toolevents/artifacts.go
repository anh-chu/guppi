package toolevents

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

)

var (
	artifactANSIRE  = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-9;?]*[a-zA-Z]`)
	artifactNoiseRE = regexp.MustCompile(`/(?:node_modules|dist|\.git|__pycache__|\.cache)/`)
	artifactOSC8RE  = regexp.MustCompile(`\x1b]8;[^;\x07\x1b]*;(file://[^\x07\x1b]*)(?:\x07|\x1b\\)`)
	artifactOSC7RE  = regexp.MustCompile(`\x1b]7;file://([^/\x07\x1b]*)(/[^\x07\x1b]*)(?:\x07|\x1b\\)`)
)

const artifactExt = `(?:png|jpe?g|gif|svg|webp|pdf|csv|tsv|json|md|txt|log|ya?ml|diff|patch|html?|py|go|ts|tsx|js|jsx|sh|toml|ini|conf|xml|zip|tar|gz)`

var artifactTokenRE = regexp.MustCompile(`^(?:(?:~|\.{1,2})?/[` + `\w.\-/]+\.` + artifactExt + `|[` + `\w.\-]+\.` + artifactExt + `)$`)

var (
	localHostnameOnce sync.Once
	localHostname     string
)

func localHostnameValue() string {
	localHostnameOnce.Do(func() {
		localHostname, _ = os.Hostname()
	})
	return localHostname
}

func isLocalHostname(h string) bool {
	switch {
	case h == "":
		return true
	case strings.EqualFold(h, "localhost"):
		return true
	default:
		return h == localHostnameValue()
	}
}

func decodeLocalFileURI(uri string) (string, bool) {
	if !strings.HasPrefix(uri, "file://") {
		return "", false
	}
	rest := strings.TrimPrefix(uri, "file://")
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return "", false
	}
	host := rest[:slash]
	if !isLocalHostname(host) {
		return "", false
	}
	path, err := url.PathUnescape(rest[slash:])
	if err != nil || path == "" {
		return "", false
	}
	return path, true
}

func isArtifactSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '"', '\'', '`', '(', ')', '[', ']', '{', '}', ':', '=', ',', ';':
		return true
	default:
		return false
	}
}

// ScanArtifactPaths strips ANSI escapes and extracts path-ish file tokens from
// text using the same heuristic as the old client-side fallback.
func ScanArtifactPaths(text string) []string {
	if text == "" {
		return nil
	}
	text = artifactANSIRE.ReplaceAllString(text, "")
	tokens := strings.FieldsFunc(text, isArtifactSeparator)
	if len(tokens) == 0 {
		return nil
	}
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token == "" || len(token) > 200 || artifactNoiseRE.MatchString(token) {
			continue
		}
		if artifactTokenRE.MatchString(token) {
			out = append(out, token)
		}
	}
	return out
}

// ParseOSC8FilePaths returns local file paths from OSC 8 hyperlinks.
func ParseOSC8FilePaths(text string) []string {
	if text == "" {
		return nil
	}
	matches := artifactOSC8RE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		if path, ok := decodeLocalFileURI(match[1]); ok {
			out = append(out, path)
		}
	}
	return out
}

// ParseOSC7CWD returns the last local cwd report from OSC 7.
func ParseOSC7CWD(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	matches := artifactOSC7RE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return "", false
	}
	match := matches[len(matches)-1]
	if len(match) < 3 {
		return "", false
	}
	host := match[1]
	path := match[2]
	if !isLocalHostname(host) {
		return "", false
	}
	decoded, err := url.PathUnescape(path)
	if err != nil || decoded == "" {
		return "", false
	}
	return decoded, true
}

func displayPathForHome(path, home string) string {
	if path == "" || home == "" {
		return ""
	}
	home = filepath.Clean(home)
	path = filepath.Clean(path)
	if path == home {
		return "~"
	}
	prefix := home + string(filepath.Separator)
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	return "~" + strings.TrimPrefix(path, home)
}

func displayPathForResolved(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return displayPathForHome(path, home)
}


// CWDResolver returns the working directory for a session.
type CWDResolver interface {
	SessionCWD(session string) string
}

// ResolveSessionCWD returns the session's working directory.
func ResolveSessionCWD(resolver CWDResolver, session string) string {
	if resolver == nil || session == "" {
		return ""
	}
	return resolver.SessionCWD(session)
}

func resolveAgainstBase(base, p string) string {
	if base != "" && !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// ResolveArtifactPath resolves p against cwd or accepts absolute paths.
// Absolute paths are resolved and symlink-checked (regular file stat in caller).
// Relative paths and ~ are resolved against cwd. The deployment is fully trusted;
// ResolveArtifactPath is a sanity check (symlink validity, regular file), not a boundary.
func ResolveArtifactPath(cwd, p string) (string, bool) {
	if p == "" {
		return "", false
	}
	var candidate string
	if filepath.IsAbs(p) {
		// Absolute path: accepted anywhere, just resolve symlinks
		candidate = filepath.Clean(p)
	} else {
		// Relative path or ~: resolve against cwd
		if cwd == "" {
			return "", false
		}
		candidate = resolveAgainstBase(filepath.Clean(cwd), p)
	}
	// Resolve symlinks; stat happens in caller
	candidateReal, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false
	}
	return candidateReal, true
}

// EnrichArtifact resolves one path against cwd, stats it locally, and returns
// a file artifact only when the path exists and is a regular file.
// Paths outside cwd are dropped rather than stored with a false "exists" flag.
func EnrichArtifact(path, cwd string, tool Tool, source string) *FileArtifact {
	if cwd == "" || path == "" {
		return nil
	}
	resolved, ok := ResolveArtifactPath(cwd, path)
	if !ok {
		return nil
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		return nil
	}
	return &FileArtifact{
		Path:        resolved,
		DisplayPath: displayPathForResolved(resolved),
		Name:        filepath.Base(resolved),
		Size:        info.Size(),
		ModTime:     info.ModTime(),
		Tool:        tool,
		Source:      source,
		FirstSeen:   time.Now(),
	}
}

// EnrichArtifacts resolves each path against cwd, stats it locally, and
// returns only entries that exist and are regular files.
// Paths outside cwd are dropped rather than stored with a false "exists" flag.
func EnrichArtifacts(paths []string, cwd string, tool Tool, source string) []*FileArtifact {
	if len(paths) == 0 {
		return nil
	}
	out := make([]*FileArtifact, 0, len(paths))
	for _, p := range paths {
		if art := EnrichArtifact(p, cwd, tool, source); art != nil {
			out = append(out, art)
		}
	}
	return out
}
