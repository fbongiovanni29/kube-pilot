package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Document struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var (
	store = make(map[string]Document)
	mu    sync.RWMutex
)

func main() {
	http.HandleFunc("/docs", handleDocs)
	http.HandleFunc("/docs/", handleDocByID)
	log.Println("docs-api listening on :8082")
	log.Fatal(http.ListenAndServe(":8082", nil))
}

func handleDocs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		mu.RLock()
		docs := make([]Document, 0, len(store))
		for _, d := range store {
			docs = append(docs, d)
		}
		mu.RUnlock()
		writeJSON(w, http.StatusOK, docs)

	case http.MethodPost:
		var input struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		doc := Document{
			ID:        uuid.New().String(),
			Title:     input.Title,
			Body:      input.Body,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		mu.Lock()
		store[doc.ID] = doc
		mu.Unlock()
		writeJSON(w, http.StatusCreated, doc)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleDocByID(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/docs/"):]
	if id == "" {
		http.Error(w, "missing document id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		mu.RLock()
		doc, ok := store[id]
		mu.RUnlock()
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, doc)

	case http.MethodPut:
		var input struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		mu.Lock()
		doc, ok := store[id]
		if !ok {
			mu.Unlock()
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		doc.Title = input.Title
		doc.Body = input.Body
		doc.UpdatedAt = time.Now()
		store[id] = doc
		mu.Unlock()
		writeJSON(w, http.StatusOK, doc)

	case http.MethodDelete:
		mu.Lock()
		delete(store, id)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
