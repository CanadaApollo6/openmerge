package forge

import "context"

// RequestClass identifies the work a request serves. Drivers reserve capacity
// per installation, including independent shares for discovery and rule checks.
type RequestClass uint8

// Request classes distinguish settlement, active queues and background work.
const (
	IdleRequest RequestClass = iota
	SettlementRequest
	ActiveRequest
	DiscoveryRequest
	RulesRequest
	ReportingRequest
)

type requestClassKey struct{}

// WithRequestClass scopes every nested request, including token refreshes.
func WithRequestClass(ctx context.Context, class RequestClass) context.Context {
	return context.WithValue(ctx, requestClassKey{}, class)
}

// ClassOfRequest defaults unclassified callers to idle polling.
func ClassOfRequest(ctx context.Context) RequestClass {
	class, _ := ctx.Value(requestClassKey{}).(RequestClass)
	return class
}
