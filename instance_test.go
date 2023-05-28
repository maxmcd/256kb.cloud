package main

import (
	"context"
	_ "embed"
	"testing"
)

func BenchmarkInstance(b *testing.B) {
	b.Run("filecache", func(b *testing.B) {
		cacheDir := b.TempDir()
		for i := 0; i < b.N; i++ {
			i, err := NewInstance(context.Background(), counterWasm, cacheDir)
			if err != nil {
				b.Fatal(err)
			}
			if err := i.runtime.Close(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("no cache", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			i, err := NewInstance(context.Background(), counterWasm, "")
			if err != nil {
				b.Fatal(err)
			}
			if err := i.runtime.Close(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
}
