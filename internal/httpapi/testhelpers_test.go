package httpapi

// ptr returns a pointer to a value, for optional PATCH fields in tests.
func ptr[T any](value T) *T { return &value }
