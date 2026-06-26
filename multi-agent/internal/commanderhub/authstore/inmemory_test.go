package authstore

import "testing"

func TestInMemoryStore_Conformance(t *testing.T) {
	RunConformanceTests(t, func(t *testing.T) Store {
		return NewInMemoryStore()
	})
}
