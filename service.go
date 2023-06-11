package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	_ "embed"
	"encoding/base32"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/julienschmidt/httprouter"
	"golang.org/x/exp/slog"
)

var (

	//go:embed templates/*.html
	templatesFS        embed.FS
	tinyGoBuildCommand = "tinygo build -x -o main.wasm -target wasi main.go"
	wsUpgrader         = websocket.Upgrader{}
)

type Service struct {
	runtime *Runtime
	t       *template.Template
	cfg     Config
	builder *Builder
	dataDir string

	tinygoVersion string
}

type Config struct {
	host          string
	subdomainHost string
	dev           bool
	port          int
}

type MP map[string]interface{}

func mkdirIfNoExist(path string) error {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return os.Mkdir(path, 0777)
	}
	if !fi.IsDir() {
		return fmt.Errorf("Path %q already exists and is not a directory", path)
	}
	return nil
}

func NewService(cfg Config, dataDir string) (*Service, error) {
	var t *template.Template
	t, err := template.New("").Funcs(template.FuncMap{
		"yield": func(data interface{}) (template.HTML, error) {
			name := data.(MP)["inner_template"].(string)
			var buf bytes.Buffer
			err := t.ExecuteTemplate(&buf, name, data)
			return template.HTML(buf.String()), err
		},
	}).ParseFS(templatesFS, "templates/*")
	if err != nil {
		return nil, fmt.Errorf("error parsing templates: %w", err)
	}
	s := &Service{
		t:       t,
		cfg:     cfg,
		runtime: NewRuntime(filepath.Join(dataDir, "module-cache")),
		builder: NewBuilder(),
		dataDir: dataDir,
	}
	if err := mkdirIfNoExist(s.appDir()); err != nil {
		return nil, err
	}
	if err := mkdirIfNoExist(filepath.Join(dataDir, "module-cache")); err != nil {
		return nil, err
	}

	b, err := exec.Command("tinygo", "version").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("error running 'tinygo version': %w: %s", err, string(b))
	}
	s.tinygoVersion = string(b)

	if err := s.runtime.LoadApplications(context.TODO(), s.appDir()); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Service) errorResp(w http.ResponseWriter, code int, err error) {
	w.Header().Add("Content-Type", "text/html")
	w.WriteHeader(code)
	if err == nil {
		err = fmt.Errorf(http.StatusText(code))
	}
	slog.Error("errorResp", "err", err)
	s.executeTemplate(w, "error.html",
		MP{
			"error":       err,
			"code":        code,
			"status_text": http.StatusText(code),
		},
	)
}
func (s *Service) executeTemplate(w http.ResponseWriter, name string, data MP) {
	if data == nil {
		data = MP{}
	}
	data["inner_template"] = name
	s.executePartial(w, "application.html", data)
}

func (s *Service) executePartial(w http.ResponseWriter, name string, data MP) {
	if data == nil {
		data = MP{}
	}
	data["dev"] = s.cfg.dev
	if err := s.t.ExecuteTemplate(errSwallowingWriter{Writer: w}, name, data); err != nil {
		slog.Error("Error rendering template", err, "name", name)
		s.errorResp(w, http.StatusInternalServerError, err)
	}
}

type errSwallowingWriter struct {
	io.Writer
}

// Ignore write errors on render, we only want to deal with template execution
// errors.
func (e errSwallowingWriter) Write(b []byte) (n int, err error) {
	n, _ = e.Writer.Write(b)
	return n, nil
}

func (s *Service) newHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	serverSource := counterSrc
	htmlSource := counterHTML

	from := r.URL.Query().Get("from")
	fromWrap := func(err error) error { return fmt.Errorf("error fetching source from existing application: %w", err) }
	if from != "" {
		var err error
		if htmlSource, err = s.readAppFile(from, "index.html"); err != nil {
			s.errorResp(w, http.StatusNotFound, fromWrap(err))
			return
		}
		if serverSource, err = s.readAppFile(from, "main.go"); err != nil {
			s.errorResp(w, http.StatusNotFound, fromWrap(err))
			return
		}
	}

	s.executeTemplate(w, "new.html", MP{
		"source_filename": "main.go",
		"server_source":   string(serverSource),
		"html_source":     string(htmlSource),
	})
}

func (s *Service) buildStatus(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	s.partialErrorHandler(w, "_build_status.html", func() (data MP, err error) {
		build := s.builder.Get(r.URL.Query().Get("hash"))
		if build == nil {
			return nil, fmt.Errorf("Build not found")
		}
		data = MP{"build": build.templateData()}
		if !data["build"].(MP)["completed"].(bool) {
			// Build is ongoing, send status
			return data, nil
		}
		if *data["build"].(MP)["exit_code"].(*int) != 0 {
			// Build failed
			_ = os.RemoveAll(build.dir)
			return data, nil
		}

		build.Lock()
		defer build.Unlock()
		// TODO: also try and run the wasm here, return any errors. We should
		// not deploy things that will fail to run.
		slog.Info("Build complete, starting application", "id", build.Hash)
		if _, err := os.Stat(build.dir); os.IsNotExist(err) {
			return nil, fmt.Errorf("build directory is missing")
		}
		s.builder.Delete(build.Hash)
		appDir := s.appDir(build.Hash)
		if _, err := os.Stat(appDir); !os.IsNotExist(err) {
			// Cleanup if this app already exists.
			_ = os.RemoveAll(build.dir)
		}
		if err := os.Rename(build.dir, appDir); err != nil {
			return nil, fmt.Errorf("error creating app dir: %w", err)
		}

		fmt.Fprintln(build.Logs, "Starting application")
		if err := s.runtime.AddApplication(context.TODO(), appDir); err != nil {
			fmt.Fprintf(build.Logs, "Error starting application: %v\n", err)
			data["build"].(MP)["error"] = err.Error()
			_ = os.RemoveAll(appDir)
		}
		return data, nil
	})
}

type errorHandlerCB func() (data MP, err error)

func (s *Service) _tErrorHandler(handler func(w http.ResponseWriter, name string, data MP),
	w http.ResponseWriter,
	name string,
	cb errorHandlerCB,
) {
	data, err := cb()
	if data == nil {
		data = MP{}
	}
	if err != nil {
		data["error"] = err
	}
	handler(w, name, data)
}
func (s *Service) partialErrorHandler(w http.ResponseWriter, name string, cb errorHandlerCB) {
	s._tErrorHandler(s.executePartial, w, name, cb)
}
func (s *Service) templateErrorHandler(w http.ResponseWriter, name string, cb errorHandlerCB) {
	s._tErrorHandler(s.executeTemplate, w, name, cb)
}

func (s *Service) createHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	s.partialErrorHandler(w, "_build_status.html", func() (data MP, err error) {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		data = MP{}
		buildDir, err := os.MkdirTemp("", "")
		if err != nil {
			return data, err
		}

		if err := os.WriteFile(
			filepath.Join(buildDir, r.FormValue("source_filename")),
			[]byte(r.FormValue("server_source")),
			0666,
		); err != nil {
			return data, err
		}
		if err := os.WriteFile(
			filepath.Join(buildDir, "index.html"),
			[]byte(r.FormValue("html_source")),
			0666); err != nil {
			return data, err
		}
		hash, err := hashResult(tinyGoBuildCommand, s.tinygoVersion, r.FormValue("server_source"), r.FormValue("html_source"))
		if err != nil {
			return nil, err
		}
		if s.runtime.GetInstance(hash) != nil {
			data["previous_result"] = MP{"hash": hash}
		}
		build := s.builder.SubmitBuild(hash, buildDir, tinyGoBuildCommand)
		data["build"] = build.templateData()
		return data, nil
	})
}
func (s *Service) wsHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	subdomain, found := strings.CutSuffix(r.Host, "."+s.cfg.subdomainHost)
	if !found || subdomain == "" {
		s.errorResp(w, http.StatusNotFound, fmt.Errorf("not found"))
		return
	}
	i := s.runtime.GetInstance(subdomain)
	if i == nil {
		s.errorResp(w, http.StatusNotFound, fmt.Errorf("not found"))
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.errorResp(w, http.StatusBadRequest, err)
		return
	}
	go func() {
		for range time.NewTicker(time.Second).C {
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second)); err != nil {
				return
			}
		}
	}()
	wsConn := websocketConnWriter{conn: conn}
	connID, err := i.NewConn(context.TODO(), WebsocketConnectionType, wsConn)
	if err != nil {
		s.errorResp(w, http.StatusInternalServerError, err)
		return
	}
	for {
		_, reader, err := conn.NextReader()
		if err != nil {
			break
		}
		wsConn.nextReader = reader
		_ = i.OnConnRead(r.Context(), connID)
	}
	_ = i.OnConnClose(context.TODO(), connID)
}

type websocketConnWriter struct {
	conn       *websocket.Conn
	nextReader io.Reader
}

var _ io.ReadWriteCloser = websocketConnWriter{}

func (w websocketConnWriter) Read(b []byte) (n int, err error) {
	return w.nextReader.Read(b)
}
func (w websocketConnWriter) Write(b []byte) (n int, err error) {
	return len(b), w.conn.WriteMessage(websocket.BinaryMessage, b)
}
func (w websocketConnWriter) Close() (err error) {
	return w.conn.Close()
}
func (s *Service) appInfoHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	s.templateErrorHandler(w, "info.html", func() (data MP, err error) {
		appName := p.ByName("name")
		appDir := s.appDir(appName)
		if _, err := os.Stat(appDir); os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			return nil, fmt.Errorf("application %q not found", appName)
		}

		serverSrc, err := os.ReadFile(filepath.Join(appDir, "main.go"))
		if err != nil {
			return nil, fmt.Errorf("error reading main.go: %w", err)
		}
		htmlSrc, err := os.ReadFile(filepath.Join(appDir, "index.html"))
		if err != nil {
			return nil, fmt.Errorf("error reading index.html: %w", err)
		}
		return MP{
			"name":           appName,
			"subdomain_host": s.cfg.subdomainHost,
			"server_source":  string(serverSrc),
			"html_source":    string(htmlSrc),
			"iframe":         "//" + appName + "." + s.cfg.subdomainHost,
		}, nil
	})
}

func (s *Service) appDir(path ...string) string {
	return filepath.Join(append([]string{s.dataDir, "apps"}, path...)...)
}

func (s *Service) indexHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	entries, err := ioutil.ReadDir(s.appDir())
	if err != nil {
		s.errorResp(w, http.StatusInternalServerError, err)
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ModTime().After(entries[j].ModTime()) })
	apps := []MP{}
	for _, e := range entries {
		apps = append(apps, MP{
			"name": e.Name(),
			"date": e.ModTime().Format(time.RFC3339),
		})
	}
	if len(apps) > 50 {
		apps = apps[:50]
	}
	s.executeTemplate(w, "index.html", MP{"apps": apps})
}

func (s *Service) appHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	appName := p.ByName("name")
	if s.runtime.GetInstance(appName) == nil {
		s.errorResp(w, http.StatusNotFound, nil)
		return
	}
	s.executeTemplate(w, "app.html", MP{
		"hide_nav": true,
		"name":     appName,
		"iframe":   "//" + appName + "." + s.cfg.subdomainHost,
	})
}

func (s *Service) methodNotAllowedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.errorResp(w, http.StatusMethodNotAllowed, fmt.Errorf("Method not allowed"))
	})
}

func (s *Service) readAppFile(appName, filename string) (src []byte, err error) {
	appDir := s.appDir(appName)
	if _, err := os.Stat(appDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("application %q not found", appName)
	}

	htmlSrc, err := os.ReadFile(filepath.Join(appDir, filename))
	if err != nil {
		return nil, fmt.Errorf("error reading index.html: %w", err)
	}
	return htmlSrc, nil
}

func (s *Service) appSourceHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	appName, _ := strings.CutSuffix(r.Host, "."+s.cfg.subdomainHost)
	htmlSrc, err := s.readAppFile(appName, "index.html")
	if err != nil {
		s.errorResp(w, http.StatusNotFound, err)
		return
	}
	_, _ = w.Write(htmlSrc)
}

func (s *Service) Handler() http.Handler {
	// Main site router.
	mainRouter := httprouter.New()
	// Separate router to get around overlapping wildcard rules.
	namedPathRouter := httprouter.New()
	mainRouter.MethodNotAllowed = namedPathRouter
	mainRouter.NotFound = namedPathRouter
	namedPathRouter.MethodNotAllowed = s.methodNotAllowedHandler()
	namedPathRouter.NotFound = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.errorResp(w, http.StatusNotFound, nil)
	})
	// Router for apps serving on their own domains
	appSubdomainRouter := httprouter.New()
	appSubdomainRouter.MethodNotAllowed = s.methodNotAllowedHandler()
	appSubdomainRouter.NotFound = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.errorResp(w, http.StatusNotFound, nil)
	})

	namedPathRouter.GET("/new", s.newHandler)
	namedPathRouter.GET("/health", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		fmt.Fprint(w, "I love you. I'm glad I exist.")
	})

	namedPathRouter.GET("/new/build-status", s.buildStatus)
	namedPathRouter.POST("/new", s.createHandler)

	appSubdomainRouter.GET("/", s.appSourceHandler)
	appSubdomainRouter.GET("/ws", s.wsHandler)

	mainRouter.GET("/", s.indexHandler)
	mainRouter.GET("/:name", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		// httprouter doesn't support overlapping parameter names and named
		// routes so first check if this is a route name we have reserved.
		handle, params, _ := namedPathRouter.Lookup(http.MethodGet, r.URL.Path)
		if handle != nil {
			handle(w, r, params)
			return
		}
		s.appHandler(w, r, p)
	})
	mainRouter.GET("/:name/info", s.appInfoHandler)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.Host, s.cfg.subdomainHost) {
			appSubdomainRouter.ServeHTTP(w, r)
		} else {
			mainRouter.ServeHTTP(w, r)
		}
	})
}

func makeAppDir() (dir string, err error) {
	user, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("error getting current user: %w", err)
	}
	appDir := filepath.Join(user.HomeDir, ".256kb")
	if fi, err := os.Stat(appDir); os.IsNotExist(err) {
		if err := os.Mkdir(appDir, 0777); err != nil {
			return "", fmt.Errorf("error making directory %q: %w", appDir, err)
		}
	} else if !fi.IsDir() {
		return "", fmt.Errorf("data directory location %q already exists but it is not a directory", err)
	}
	return appDir, nil
}

func main() {
	cfg := Config{
		host: "localhost:3001",
	}
	flag.StringVar(&cfg.host, "host", "localhost:3001", "The HTTP Host the application will accept requests from.")
	flag.StringVar(&cfg.subdomainHost, "subdomain-host", "",
		"The HTTP Host where application subdomains will be served, defaults to value of 'host' if unset")
	flag.BoolVar(&cfg.dev, "dev", false, "Run the server in development mode")
	flag.IntVar(&cfg.port, "port", 3000, "Listen to me...")
	flag.Parse()
	if cfg.subdomainHost == "" {
		cfg.subdomainHost = cfg.host
	}

	appDir, err := makeAppDir()
	if err != nil {
		log.Panicln(err)
	}

	service, err := NewService(cfg, appDir)
	if err != nil {
		log.Panicln(err)
	}
	_ = service

	slog.Info("Listening", "port", cfg.port)
	panic(http.ListenAndServe(":"+fmt.Sprint(cfg.port), logMiddleware(service.Handler())))
}

func hashResult(in ...string) (string, error) {
	h := sha256.New()
	for _, i := range in {
		if _, err := fmt.Fprint(h, i); err != nil {
			// TODO: will this ever fail?
			return "", err
		}
	}
	return bytesToBase32Hash(h.Sum(nil)), nil
}

// BytesToBase32Hash copies nix here
// https://nixos.org/nixos/nix-pills/nix-store-paths.html
// The comments tell us to compute the base32 representation of the
// first 160 bits (truncation) of a sha256 of the above string:
func bytesToBase32Hash(b []byte) string {
	var buf bytes.Buffer
	_, _ = base32.NewEncoder(base32.StdEncoding, &buf).Write(b[:20])
	return strings.ToLower(buf.String())
}
