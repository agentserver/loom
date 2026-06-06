package identity

import (
	"context"
	"errors"
)

type chain struct {
	resolvers []Resolver
}

func NewChain(resolvers ...Resolver) Resolver {
	if len(resolvers) == 0 {
		panic("identity: empty resolver chain")
	}
	for _, resolver := range resolvers {
		if resolver == nil {
			panic("identity: nil resolver in chain")
		}
	}
	return chain{resolvers: append([]Resolver(nil), resolvers...)}
}

func (c chain) Resolve(ctx context.Context, token string) (Identity, error) {
	for _, resolver := range c.resolvers {
		ident, err := resolver.Resolve(ctx, token)
		if err == nil {
			return ident, nil
		}
		if errors.Is(err, ErrInvalid) {
			continue
		}
		return Identity{}, err
	}
	return Identity{}, ErrInvalid
}
