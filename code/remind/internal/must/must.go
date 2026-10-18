// Package must turns errors that can't happen into panics.
package must

// Get returns v, or panics if err is not nil. Use it only where an error
// would mean a bug, such as parsing a constant.
func Get[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
