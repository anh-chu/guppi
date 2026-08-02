//go:build !linux && !darwin

package server

// newForegroundProvider returns an explicit unsupported provider on platforms
// that lack process discovery. Callers treat false as "no foreground process".
func newForegroundProvider() ForegroundProvider {
	return unsupportedForegroundProvider{}
}

type unsupportedForegroundProvider struct{}

func (unsupportedForegroundProvider) Foreground(shellPid int) (string, bool) {
	return "", false
}
