package main

import "testing"

func TestNewService(t *testing.T) {
	s, err := NewService(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_ = s
}
