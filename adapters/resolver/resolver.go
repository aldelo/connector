package resolver

import (
	"google.golang.org/grpc/resolver"
	"sync"
)

var builder *customResolverBuilder

// package level init, only called once and is not thread safe
func init() {
	builder = new(customResolverBuilder)
	builder.mux = new(sync.RWMutex)
	resolver.Register(builder)
}

// Update will update resolve data, always call update before using resolver
func Update(schemeName string, serviceName string, endpointAddrs []string) {
	if builder != nil {
		builder.mux.Lock()
		defer builder.mux.Unlock()

		builder.SchemeName = schemeName
		builder.ServiceName = serviceName
		builder.EndpointAddrs = endpointAddrs
	}
}

// ---------------------------------------------------------------------------------------------------------------------

type customResolverBuilder struct{
	SchemeName string
	ServiceName string
	EndpointAddrs []string

	mux *sync.RWMutex
}

func (b *customResolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, opts resolver.BuildOptions) (resolver.Resolver, error) {
	b.mux.RLock()
	defer b.mux.RUnlock()

	r := &customResolver{
		target: target,
		cc:     cc,
		addrsStore: map[string][]string{
			b.ServiceName: b.EndpointAddrs,
		},
	}
	r.start()
	return r, nil
}
func (b *customResolverBuilder) Scheme() string {
	b.mux.RLock()
	defer b.mux.RUnlock()

	return b.SchemeName
}

// ---------------------------------------------------------------------------------------------------------------------

type customResolver struct {
	target     resolver.Target
	cc         resolver.ClientConn
	addrsStore map[string][]string
}

func (r *customResolver) start() {
	addrStrs := r.addrsStore[r.target.Endpoint]
	addrs := make([]resolver.Address, len(addrStrs))

	for i, s := range addrStrs {
		addrs[i] = resolver.Address{Addr: s}
	}

	r.cc.UpdateState(resolver.State{Addresses: addrs})
}

func (r *customResolver) ResolveNow(o resolver.ResolveNowOptions) {}

func (r *customResolver) Close(){}
