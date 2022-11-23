package cache

// ScopedCacheOption is a function to set values on the ScopedCache
type ScopedCacheOption func(*ScopedCache)

// WithScopedInformerFactory is an option that can be used
// to set the ScopedCache.scopedInformerFactory field when
// creating a new ScopedCache.
func WithScopedInformerFactory(sif ScopeInformerFactory) ScopedCacheOption {
	return func(sc *ScopedCache) {
		sc.scopeInformerFactory = sif
	}
}
