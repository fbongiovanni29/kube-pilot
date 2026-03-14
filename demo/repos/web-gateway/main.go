package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

type route struct {
	prefix string
	target string
}

func main() {
	authURL := envOrDefault("AUTH_SERVICE_URL", "http://localhost:8081")
	docsURL := envOrDefault("DOCS_API_URL", "http://localhost:8082")

	routes := []route{
		{prefix: "/auth/", target: authURL},
		{prefix: "/api/docs", target: docsURL},
	}

	mux := http.NewServeMux()

	for _, r := range routes {
		proxy := newProxy(r.target, r.prefix)
		mux.Handle(r.prefix, proxy)
		log.Printf("route %s -> %s", r.prefix, r.target)
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"service":"web-gateway","status":"ok","routes":["/auth/","/api/docs"]}`))
	})

	log.Println("web-gateway listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func newProxy(target, prefix string) http.Handler {
	targetURL, err := url.Parse(target)
	if err != nil {
		log.Fatalf("invalid target url %s: %v", target, err)
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.URL.Path = stripPrefix(req.URL.Path, prefix)
			req.Host = targetURL.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy error for %s: %v", r.URL.Path, err)
			http.Error(w, `{"error":"upstream unavailable"}`, http.StatusBadGateway)
		},
	}
	return proxy
}

func stripPrefix(path, prefix string) string {
	stripped := strings.TrimPrefix(path, strings.TrimSuffix(prefix, "/"))
	if stripped == "" || stripped[0] != '/' {
		stripped = "/" + stripped
	}
	return stripped
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
