package main

import (
	"bytes"
	"context"
	"embed"
	_ "embed"
	"encoding/base32"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
	"golang.org/x/exp/slog"

	"github.com/julienschmidt/httprouter"
)

//go:embed templates/*.html
var templatesFS embed.FS

type MP map[string]interface{}

var upgrader = websocket.Upgrader{}

func (s *Service) errorResp(w http.ResponseWriter, code int, err error) {
	w.WriteHeader(code)
	if err == nil {
		err = fmt.Errorf(http.StatusText(code))
	}
	if err := s.t.ExecuteTemplate(w, "error.html", MP{"error": err, "dev": s.cfg.dev}); err != nil {
		slog.Error("Error rendering template", err, "name", "error.html")
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type Service struct {
	t       *template.Template
	cfg     Config
	builder *Builder
}

type Config struct {
	host string
	dev  bool
}

func NewService(cfg Config) (*Service, error) {
	var t *template.Template
	t, err := template.New("").Funcs(template.FuncMap{
		"partial": func(name string, data interface{}) (template.HTML, error) {
			var buf bytes.Buffer
			err := t.ExecuteTemplate(&buf, name, data)
			return template.HTML(buf.String()), err
		},
	}).ParseFS(templatesFS, "templates/*")
	if err != nil {
		return nil, fmt.Errorf("error parsing templates: %w", err)
	}

	return &Service{t: t, cfg: cfg, builder: NewBuilder()}, nil
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
	if err := s.t.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("Error rendering template", err, "name", name)
		s.errorResp(w, http.StatusInternalServerError, err)
	}
}

func (s *Service) newHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	s.executeTemplate(w, "new.html", MP{
		"source_filename": "main.go",
		"server_source":   string(counterSrc),
		"html_source":     string(counterHTML),
	})
}

func (s *Service) buildStatus(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	buildID, _ := strconv.Atoi(r.URL.Query().Get("id"))
	build := s.builder.Get(buildID)
	data := MP{"build": build}
	if build == nil {
		data = MP{"error": "Build not found"}
	}
	s.executePartial(w, "_build_status.html", data)
}

func (s *Service) partialErrorHandler(w http.ResponseWriter, name string, cb func() (data MP, err error)) {
	data, err := cb()
	if data == nil {
		data = MP{}
	}
	if err != nil {
		data["error"] = err
	}
	s.executePartial(w, name, data)
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
		build := s.builder.SubmitBuild(buildDir, "tinygo build -x -o main.wasm -target wasi main.go")
		data["build"] = build
		return data, nil
	})
}
func (s *Service) wsHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	subdomain, found := strings.CutSuffix(r.Host, s.cfg.host)
	if !found || subdomain == "" {
		s.errorResp(w, http.StatusNotFound, fmt.Errorf("not found"))
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.errorResp(w, http.StatusBadRequest, err)
		return
	}
	for {
		_, msg, err := conn.ReadMessage()
		fmt.Println(msg, err)
		if err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("1"))
	}
}

func (s *Service) appInfoHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	s.executeTemplate(w, "info.html", MP{"name": p.ByName("name")})
}

func (s *Service) appHandler(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	name := p.ByName("name")
	s.executeTemplate(w, "app.html", MP{
		"name":   name,
		"iframe": "//" + name + "." + s.cfg.host,
	})
}

func (s *Service) methodNotAllowedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.errorResp(w, http.StatusMethodNotAllowed, fmt.Errorf("Method not allowed"))
	})
}

func (s *Service) Handler() http.Handler {
	mainRouter := httprouter.New()
	commonPagesRouter := httprouter.New()
	mainRouter.MethodNotAllowed = commonPagesRouter
	mainRouter.NotFound = commonPagesRouter
	commonPagesRouter.MethodNotAllowed = s.methodNotAllowedHandler()
	appRouter := httprouter.New()
	appRouter.MethodNotAllowed = s.methodNotAllowedHandler()

	commonPagesRouter.GET("/new", s.newHandler)
	commonPagesRouter.GET("/new/build-status", s.buildStatus)
	commonPagesRouter.POST("/new", s.createHandler)

	appRouter.GET("/", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		_, _ = w.Write(counterHTML)
	})
	appRouter.GET("/ws", s.wsHandler)

	mainRouter.GET("/", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		s.executeTemplate(w, "index.html", nil)
	})
	mainRouter.GET("/:name", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		// httprouter doesn't support overlapping parameter names and named
		// routes so first check if this is a route name we have reserved.
		handle, params, _ := commonPagesRouter.Lookup(http.MethodGet, r.URL.Path)
		if handle != nil {
			handle(w, r, params)
			return
		}
		s.appHandler(w, r, p)
	})
	mainRouter.GET("/:name/info", s.appInfoHandler)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if we're serving from a subdomain like app_name.256kb.
		subdomain, found := strings.CutSuffix(r.Host, s.cfg.host)
		if !found {
			s.errorResp(w, http.StatusNotFound, fmt.Errorf("not found"))
			return
		}
		// If we aren't, or if it's a known subdomain, use the main router.
		if subdomain == "www" || subdomain == "" {
			mainRouter.ServeHTTP(w, r)
		} else {
			appRouter.ServeHTTP(w, r)
		}
	})

}

func main() {
	cfg := Config{
		host: "localhost:3001",
	}
	flag.StringVar(&cfg.host, "host", "localhost:3001", "The HTTP Host the application will accept requests from.")
	flag.BoolVar(&cfg.dev, "dev", false, "Run the server in development mode")
	flag.Parse()

	service, err := NewService(cfg)
	if err != nil {
		log.Panicln(err)
	}

	slog.Info("Listening", "port", 3000)
	_ = http.ListenAndServe(":3000", logMiddleware(service.Handler()))

	i, err := NewInstance(context.Background(), counterWasm)
	if err != nil {
		log.Panicln(err)
	}
	if err := i.Listen(context.Background(), ":8080"); err != nil {
		log.Panicln(err)
	}
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
