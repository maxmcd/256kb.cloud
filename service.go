package main

import (
	"context"
	"embed"
	_ "embed"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"

	"github.com/julienschmidt/httprouter"
)

//go:embed templates/*
var templatesFS embed.FS

type MP map[string]interface{}

var upgrader = websocket.Upgrader{}

func (s *Service) errorResp(w http.ResponseWriter, code int, err error) {
	w.WriteHeader(code)
	if err == nil {
		err = fmt.Errorf(http.StatusText(code))
	}
	if err := s.t.ExecuteTemplate(w, "error.html", struct{ Error string }{err.Error()}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type Service struct {
	t *template.Template
}

type Config struct {
	host string
}

func NewService() (*Service, error) {
	t, err := template.ParseFS(templatesFS, "templates/*")
	if err != nil {
		return nil, fmt.Errorf("error parsing templates: %w", err)
	}
	return &Service{t: t}, nil
}

func main() {
	cfg := Config{
		host: "localhost:3001",
	}
	flag.StringVar(&cfg.host, "host", "localhost:3001", "The HTTP Host the application will accept requests from.")
	flag.Parse()

	service, err := NewService()
	if err != nil {
		log.Panicln(err)
	}

	router := httprouter.New()
	indexPageRouter := httprouter.New()
	appRouter := httprouter.New()

	indexPageRouter.GET("/new", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		_ = service.t.ExecuteTemplate(w, "new.html", MP{
			"server_source": string(counterSrc),
			"html_source":   string(counterHTML),
		})
	})

	appRouter.GET("/", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		_, _ = w.Write(counterHTML)
	})
	appRouter.GET("/ws", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		subdomain, found := strings.CutSuffix(r.Host, cfg.host)
		if !found || subdomain == "" {
			service.errorResp(w, http.StatusNotFound, fmt.Errorf("not found"))
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			service.errorResp(w, http.StatusBadRequest, err)
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
	})

	router.GET("/", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		_ = service.t.ExecuteTemplate(w, "index.html", nil)
	})
	router.GET("/:name", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		handle, params, _ := indexPageRouter.Lookup(http.MethodGet, r.URL.Path)
		if handle != nil {
			handle(w, r, params)
			return
		}
		name := p.ByName("name")
		if err := service.t.ExecuteTemplate(w, "app.html", struct {
			Name   string
			IFrame string
		}{name, "//" + name + "." + cfg.host}); err != nil {
			panic(err)
		}
	})
	router.GET("/:name/info", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		if err := service.t.ExecuteTemplate(w, "info.html", struct{ Name string }{p.ByName("name")}); err != nil {
			panic(err)
		}
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subdomain, found := strings.CutSuffix(r.Host, cfg.host)
		if !found {
			service.errorResp(w, http.StatusNotFound, fmt.Errorf("not found"))
			return
		}
		if subdomain == "www" || subdomain == "" {
			router.ServeHTTP(w, r)
		} else {
			appRouter.ServeHTTP(w, r)
		}
	})

	fmt.Println("Listening on port 3000")
	_ = http.ListenAndServe(":3000", handler)

	i, err := NewInstance(context.Background(), counterWasm)
	if err != nil {
		log.Panicln(err)
	}
	if err := i.Listen(context.Background(), ":8080"); err != nil {
		log.Panicln(err)
	}
}
