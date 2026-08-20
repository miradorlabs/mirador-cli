//go:build !unix

package auth

// lockCredentialFile is a no-op on platforms without flock (notably Windows).
// Cross-process serialization is best-effort there; the in-process refresh guard still
// applies, and the common case is a single mirador process at a time. This mirrors the
// project's other best-effort fallbacks rather than failing the operation outright.
func lockCredentialFile(path string) (func(), error) {
	return func() {}, nil
}
