package main

import "testing"

func TestNewService(t *testing.T) {
	s, err := NewService(Config{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = s
}
