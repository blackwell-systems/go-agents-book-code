package store_test

import (
	"errors"
	"testing"
	"time"

	"example.com/remind/store"
)

func TestHiddenGetDeleted(t *testing.T) {
	s := store.New(time.Now)
	r := s.Create("u1", "x", time.Now(), nil)
	if err := s.Delete(r.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(r.ID)
	if !errors.Is(err, store.ErrNotFound) || got != nil {
		t.Errorf("Get(deleted) = %v, %v; want nil, ErrNotFound", got, err)
	}
}
