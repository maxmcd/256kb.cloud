package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/exp/slog"
)

type Build struct {
	sync.Mutex

	Hash        string
	CreatedAt   time.Time
	CompletedAt time.Time
	ExitCode    *int
	Error       error
	Command     string
	dir         string
	Logs        *bytes.Buffer
}

func (b *Build) templateData() MP {
	b.Lock()
	defer b.Unlock()
	return MP{
		"hash":         b.Hash,
		"completed":    !b.CompletedAt.IsZero(),
		"time_seconds": b.timeSeconds(),
		"error":        b.Error,
		"logs":         b.Logs,
		"exit_code":    b.ExitCode,
		"command":      b.Command,
	}
}

func (b *Build) timeSeconds() string {
	return fmt.Sprint(int(time.Since(b.CreatedAt).Seconds())) + " seconds"
}

type Builder struct {
	lock   sync.Mutex
	builds map[string]*Build
}

func NewBuilder() *Builder {
	return &Builder{builds: map[string]*Build{}}
}

func (b *Builder) Get(hash string) *Build {
	b.lock.Lock()
	defer b.lock.Unlock()
	return b.builds[hash]
}

func (b *Builder) Delete(hash string) {
	b.lock.Lock()
	defer b.lock.Unlock()
	delete(b.builds, hash)
}

func (b *Builder) SubmitBuild(hash, dir, command string) *Build {
	b.lock.Lock()
	build := &Build{
		Hash:      hash,
		CreatedAt: time.Now(),
		Command:   command,
		dir:       dir,
		Logs:      &bytes.Buffer{},
	}
	b.builds[hash] = build
	b.lock.Unlock()

	go func() {
		if err := b.build(build); err != nil {
			build.Lock()
			build.Error = err
			build.Unlock()
		}
	}()
	return build
}

func (b *Builder) build(build *Build) error {
	cmd := exec.Command("bash", "-cx", build.Command)
	cmd.Stdout = build.Logs
	cmd.Stderr = build.Logs
	cmd.Dir = build.dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}

	err := cmd.Run()
	build.Lock()
	slog.Info("Build complete", "id", build.Hash)
	build.Error = err
	c := cmd.ProcessState.ExitCode()
	build.ExitCode = &c
	build.CompletedAt = time.Now()
	build.Unlock()
	return nil
}
