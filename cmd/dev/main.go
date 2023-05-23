package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/jaschaephraim/lrserver"
)

// html includes the client JavaScript
const html = `<!doctype html>
<html>
<head>
  <title>Example</title>
  <style>
  body { color: white; font-family: monospace; background: #333; }
  .error { color: red; }
  </style>
</head>
<body>
  Livereload...
  <p class=error>%s</p>
  <script src="http://localhost:35729/livereload.js"></script>
</body>
</html>`

func main() {
	// Create file watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Panicln(err)
	}

	defer watcher.Close()

	// Add dir to watcher
	for _, dir := range []string{
		"../../",
		".",
		"../examples/*/*",
	} {
		matches, err := filepath.Glob(dir)
		if err != nil {
			log.Panicln(err)
		}
		for _, match := range matches {
			err = watcher.Add(match)
			if err != nil {
				log.Panicln(err)
			}
		}
	}

	// Create and start LiveReload server
	lr := lrserver.New(lrserver.DefaultName, lrserver.DefaultPort)
	lr.SetErrorLog(nil)
	lr.SetStatusLog(nil)
	go func() { panic(lr.ListenAndServe()) }()

	loc, _ := filepath.Abs("../../")
	br := NewBuilderRunner(loc, lr)
	_ = br.buildAndStart()
	// Start goroutine that requests reload upon watcher event
	go func() {
		for {
			select {
			case event := <-watcher.Events:
				if event.Op == fsnotify.Chmod {
					continue
				}
				_ = br.buildAndStart()
			case err := <-watcher.Errors:
				log.Println(err)
			}
		}
	}()

	proxy := httputil.NewSingleHostReverseProxy(&url.URL{
		Scheme: "http",
		Host:   "localhost:3000",
	})

	// Start serving html
	http.HandleFunc("/", func(rw http.ResponseWriter, req *http.Request) {
		if err := br.Err(); err != nil {
			fmt.Fprintf(rw, html, err.Error())
			return
		}
		proxy.ServeHTTP(rw, req)
	})
	panic(http.ListenAndServe(":3001", nil))
}

type BuilderRunner struct {
	lr              *lrserver.Server
	projectLocation string
	tmpdir          string
	lock            sync.Mutex
	cmd             *exec.Cmd
	cmdRunning      bool

	err error
}

func NewBuilderRunner(projectLocation string, lr *lrserver.Server) *BuilderRunner {
	tmpdir, err := os.MkdirTemp("", "")
	if err != nil {
		log.Panicln(err)
	}
	return &BuilderRunner{
		lr:              lr,
		projectLocation: projectLocation,
		tmpdir:          tmpdir,
	}
}

func (br *BuilderRunner) _buildAndStart() error {
	// Debounce?
	if !br.lock.TryLock() {
		return nil
	}

	binaryLocation, err := br.build()
	if err != nil {
		return err
	}
	if br.cmd != nil {
		_ = br.cmd.Process.Kill()
	}
	if err := br.start(binaryLocation); err != nil {
		return err
	}
	br.lock.Unlock()
	return healthCheck()
}

func healthCheck() error {
	count := 20
	for i := 0; i < count; i++ {
		// TODO: replace this all with a custom version of Bun so that we don't
		// impact user applications, or a wrapper script
		req, err := http.Get("http://localhost:3000/health")
		if err == nil {
			_ = req.Body.Close()
			break
		}

		if i == count-1 {
			return fmt.Errorf("Process is running, but nothing is listening on the expected port")
		}
		exponent := time.Duration((i+1)*(i+1)) / 2
		time.Sleep(time.Millisecond * exponent)
	}
	return nil
}

func (br *BuilderRunner) buildAndStart() error {
	fmt.Println("-----------> Building")
	if err := br._buildAndStart(); err != nil {
		br.setError(fmt.Errorf("Error building:\n%w", err))
		return err
	}
	br.clearError()
	return nil
}

func (br *BuilderRunner) setError(err error) {
	br.lock.Lock()
	defer br.lock.Unlock()
	br.err = err
	fmt.Println("-----------> Error:")
	fmt.Println(err)
	fmt.Println("-----------> Reloading")
	br.lr.Reload("")
}

func (br *BuilderRunner) clearError() {
	br.lock.Lock()
	defer br.lock.Unlock()
	br.err = nil
	fmt.Println("-----------> Reloading")
	br.lr.Reload("")
}

func (br *BuilderRunner) Err() error {
	br.lock.Lock()
	defer br.lock.Unlock()
	return br.err
}

func (br *BuilderRunner) build() (binaryLocation string, err error) {
	binaryLocation = filepath.Join(br.tmpdir, fmt.Sprint(time.Now().UnixNano()))
	cmd := exec.Command("go", "build", "-o", binaryLocation)
	cmd.Dir = br.projectLocation
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w:\n%s", err, string(b))
	}
	return binaryLocation, nil
}

func (br *BuilderRunner) start(binaryLocation string) (err error) {
	br.cmd = exec.Command(binaryLocation)
	br.cmd.Env = os.Environ()
	br.cmd.Env = append(br.cmd.Env, "PORT=3000")
	br.cmd.Stdout = os.Stdout
	br.cmd.Stderr = os.Stderr
	defer func() {
		go func() {
			err := br.cmd.Wait()
			if err != nil && err.Error() == "signal: killed" {
				return
			}
			if err == nil {
				err = fmt.Errorf("exit status 0")
			}
			br.setError(fmt.Errorf("process exited: %w", err))
		}()
	}()
	return br.cmd.Start()
}

// func debounce(interval time.Duration, input chan string, cb func(string)) {
// 	var item string
// 	timer := time.NewTimer(0)
// 	for {
// 		select {
// 		case item = <-input:
// 			timer.Reset(interval)
// 		case <-timer.C:
// 			cb(item)
// 		}
// 	}
// }
