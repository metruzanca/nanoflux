package plugin

// StoreAuthorTokenizer adapts the plugin Registry to the store's AuthorTokenizer
// interface, so a feed's discovery mode can tell whether a post's author is one
// the user already follows through another feed. URL matching selects the plugin
// per call, so one value serves every loaded site.
type StoreAuthorTokenizer struct {
	reg *Registry
}

// NewStoreAuthorTokenizer returns a store author tokenizer over reg.
func NewStoreAuthorTokenizer(reg *Registry) StoreAuthorTokenizer {
	return StoreAuthorTokenizer{reg: reg}
}

// AuthorTokens returns the author token(s) an item's categories attribute it to
// under the owning plugin's rules, or nil when no plugin owns the URL.
func (t StoreAuthorTokenizer) AuthorTokens(feedURL string, categories []string) []string {
	if t.reg == nil {
		return nil
	}
	return t.reg.AuthorTokens(feedURL, categories)
}
