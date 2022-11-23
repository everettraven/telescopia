package errors

// InformerNotFoundErr is an error to
// represent that an informer was not
// found for the given request
type InformerNotFoundErr struct {
	Err error
}

// NewInformerNotFoundErr creates a new InformerNotFoundErr
// for the provided error
func NewInformerNotFoundErr(err error) *InformerNotFoundErr {
	return &InformerNotFoundErr{
		Err: err,
	}
}

// Error returns the string representation of the error
func (infe *InformerNotFoundErr) Error() string {
	return infe.Err.Error()
}

// IsInformerNotFoundErr checks whether or not the
// provided error is of type InformerNotFoundError
func IsInformerNotFoundErr(err error) bool {
	_, ok := err.(*InformerNotFoundErr)
	return ok
}

// CacheNotFoundErr is an error to
// represent that an object was not
// found by the cache. This could mean
// that an informer has not been created
// that would find the requested resource
type CacheNotFoundErr struct {
	Err error
}

// NewCacheNotFoundErr creates a new CacheNotFoundErr
// for the provided error
func NewCacheNotFoundErr(err error) *CacheNotFoundErr {
	return &CacheNotFoundErr{
		Err: err,
	}
}

// Error returns the string representation of the error
func (cnfe *CacheNotFoundErr) Error() string {
	return cnfe.Err.Error()
}

// IsCacheNotFoundErr checks whether or not the
// provided error is of type CacheNotFoundErr
func IsCacheNotFoundErr(err error) bool {
	_, ok := err.(*CacheNotFoundErr)
	return ok
}
