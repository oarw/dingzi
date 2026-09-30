package server

import (
	"bytes"
	"html/template"
	"mime"
	"net/http"
	"path"
	"time"
)

var loginPage = template.Must(template.New("login").ParseFS(webFS, "web/login.html"))

func (s *Server) serveLoginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_ = loginPage.ExecuteTemplate(w, "login.html", struct{ GitHub bool }{s.github != nil})
}

func embeddedPage(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := webFS.ReadFile("web/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(raw))
	}
}

func (s *Server) webRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", embeddedPage("index.html"))
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})
	mux.HandleFunc("GET /admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !s.authed(r) {
			http.Redirect(w, r, "/admin/login", http.StatusFound)
			return
		}
		embeddedPage("admin.html")(w, r)
	})
	mux.HandleFunc("GET /admin/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if s.authed(r) {
			http.Redirect(w, r, "/admin/", http.StatusFound)
			return
		}
		s.serveLoginPage(w, r)
	})
	for _, name := range []string{"base.css", "app.css", "public.js", "login.js", "theme.js"} {
		mux.HandleFunc("GET /"+name, embeddedPage(name))
	}
	for _, name := range []string{"board.js", "app.js"} {
		mux.HandleFunc("GET /admin/"+name, s.requireAuth(embeddedPage(name)))
	}
	// Vendor assets contain no panel state. Do not expose the entire embedded
	// directory: otherwise /admin.html could bypass the authenticated page route.
	mux.HandleFunc("GET /vendor/{asset...}", func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean("vendor/" + r.PathValue("asset"))
		if len(name) < len("vendor/") || name[:len("vendor/")] != "vendor/" {
			http.NotFound(w, r)
			return
		}
		embeddedPage(name)(w, r)
	})
}
