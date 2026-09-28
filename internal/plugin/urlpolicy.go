package plugin

// StoreURLPolicy adapts the plugin Registry to the store's URLPolicy interface,
// so the store (which cannot import this package) defers site-specific URL rules
// to the owning plugin. URL matching selects the plugin per call, so one value
// serves every loaded site.
type StoreURLPolicy struct {
	reg *Registry
}

// NewStoreURLPolicy returns a store URL policy over reg.
func NewStoreURLPolicy(reg *Registry) StoreURLPolicy {
	return StoreURLPolicy{reg: reg}
}

// CanonicalizeFeedURL returns the canonical stored shape of a feed URL under the
// owning plugin's rules, or the input unchanged when no plugin owns it.
func (p StoreURLPolicy) CanonicalizeFeedURL(raw string) string {
	if p.reg == nil {
		return raw
	}
	return p.reg.CanonicalizeFeedURL(raw)
}

// FeedToken returns the token a feed URL represents under the owning plugin's
// rules, or "" when no plugin owns it.
func (p StoreURLPolicy) FeedToken(feedURL string) string {
	if p.reg == nil {
		return ""
	}
	return p.reg.FeedToken(feedURL)
}
