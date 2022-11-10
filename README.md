# telescopia

`telescopia` is a Go library meant to be used in Kubernetes Operators/Controllers developed with [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime).

`telescopia` provides a dynamic caching layer that implements the controller-runtime `cache.Cache` interface. This dynamic cache allows for operator/controller authors to 
dynamically add or remove informers from the cache as needed as well as defining how informers are generated when establishing watches. This dynamic caching layer provides operator/controller authors more control over the cache and allows for new patterns of operator/controller development to occur that can't be as easily done with the currently existing controller-runtime caches. 

One such pattern (and the one it was initially created for) that `telescopia`'s dynamic cache will allow for is enabling an operator/controller to dynamically add or remove informers based on it's RBAC permissions. This allows for the creation of operators/controllers that have scopable permissions, which in turn will make operators/controllers developed with this pattern more appealing to cluster-admins and those concerned with the security of their clusters.