package forge

// BaseMovedError reports a successfully read live base that cannot reach the
// frozen build base. Read/ancestry execution failures never produce this type.
type BaseMovedError struct{ SHA string }

func (*BaseMovedError) Error() string { return "base moved; observe and replan" }
