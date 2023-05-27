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

	ID          int
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
		"id":         b.ID,
		"completed":  !b.CompletedAt.IsZero(),
		"time_since": b.timeSince(),
		"error":      b.Error,
		"logs":       b.Logs,
		"exit_code":  b.ExitCode,
		"command":    b.Command,
	}
}

func (b *Build) timeSince() string {
	return fmt.Sprint(int(time.Since(b.CreatedAt).Seconds())) + " seconds ago"
}

type Builder struct {
	lock    sync.Mutex
	counter int
	builds  map[int]*Build
}

func NewBuilder() *Builder {
	return &Builder{builds: map[int]*Build{}}
}

func (b *Builder) Get(id int) *Build {
	b.lock.Lock()
	defer b.lock.Unlock()
	return b.builds[id]
}

func (b *Builder) Delete(id int) {
	b.lock.Lock()
	defer b.lock.Unlock()
	delete(b.builds, id)
}

func (b *Builder) SubmitBuild(dir, command string) *Build {
	b.lock.Lock()
	b.counter++
	build := &Build{
		ID:        b.counter,
		CreatedAt: time.Now(),
		Command:   command,
		dir:       dir,
		Logs:      &bytes.Buffer{},
	}
	b.builds[build.ID] = build
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
	cmd := exec.Command("bash", "-c", build.Command)
	cmd.Stdout = build.Logs
	cmd.Stderr = build.Logs
	cmd.Dir = build.dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}

	err := cmd.Run()
	build.Lock()
	slog.Info("Build complete")
	build.Error = err
	c := cmd.ProcessState.ExitCode()
	build.ExitCode = &c
	build.CompletedAt = time.Now()
	build.Unlock()
	return nil
}
