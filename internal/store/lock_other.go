//go:build !unix

package store

// flockFile is a no-op on platforms without flock; the in-process keyed mutex
// still serializes operations. mrdns targets Linux in production.
func flockFile(_ string) (func(), error) {
	return func() {}, nil
}
