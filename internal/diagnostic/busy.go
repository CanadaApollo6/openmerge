package diagnostic

// BusyError marks transient local lock contention, never durable missing data.
type BusyError string

func (e BusyError) Error() string   { return string(e) }
func (e BusyError) Temporary() bool { return true }
