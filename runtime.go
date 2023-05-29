package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Runtime struct {
	lock      sync.RWMutex
	instances map[string]*Instance

	moduleCacheDir string
}

func NewRuntime(moduleCacheDir string) *Runtime {
	return &Runtime{
		moduleCacheDir: moduleCacheDir,
		instances:      map[string]*Instance{},
	}
}
func (r *Runtime) GetInstance(name string) *Instance {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.instances[name]
}

func (r *Runtime) AddApplication(ctx context.Context, path string) error {
	i, err := NewInstance(ctx, r.moduleCacheDir, path)
	if err != nil {
		return err
	}
	if err := i.Start(ctx); err != nil {
		return err
	}
	if err := i.Stop(ctx); err != nil {
		return err
	}
	r.lock.Lock()
	r.instances[filepath.Base(path)] = i
	r.lock.Unlock()
	return nil
}

func (r *Runtime) LoadApplications(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		i, err := NewInstance(ctx, r.moduleCacheDir, filepath.Join(dir, e.Name()))
		if err != nil {
			return fmt.Errorf("error instantiating app %q", e.Name())
		}
		r.lock.Lock()
		r.instances[e.Name()] = i
		r.lock.Unlock()
	}
	return nil
}
