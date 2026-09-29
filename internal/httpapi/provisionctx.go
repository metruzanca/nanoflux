package httpapi

import "context"

// provisionEntry is one create option the topbar's add menu renders: the plugin
// name (the form's plugin field) and the menu label ("Add a newsletter").
type provisionEntry struct {
	Name  string
	Label string
}

// provisionCtxKey keys the provision entries in a request context.
type provisionCtxKey struct{}

// withProvisioners stores the create options in a request context, so the
// shared topbar can render them without every page handler threading them
// through basePage.
func withProvisioners(ctx context.Context, entries []provisionEntry) context.Context {
	return context.WithValue(ctx, provisionCtxKey{}, entries)
}

// provisionersFrom reads the create options the middleware stored; empty when
// none were attached.
func provisionersFrom(ctx context.Context) []provisionEntry {
	if e, ok := ctx.Value(provisionCtxKey{}).([]provisionEntry); ok {
		return e
	}
	return nil
}
