//go:build ignore

package handlers

// Drift: drops the fmt.Errorf %w wrapping in error paths; passes plain
// concatenated strings to http.Error instead. Function shapes, signatures,
// response patterns, status codes, and validation are otherwise identical
// to S1's books.go.

import (
	"encoding/json"
	"net/http"
)

func ListAuthors(w http.ResponseWriter, r *http.Request) {
	items, err := storeListAuthors(r.Context())
	if err != nil {
		http.Error(w, "list authors: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(items); err != nil {
		http.Error(w, "encode authors: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

func GetAuthor(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	item, err := storeGetAuthor(r.Context(), id)
	if err != nil {
		http.Error(w, "get author: "+err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(item); err != nil {
		http.Error(w, "encode author: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

func CreateAuthor(w http.ResponseWriter, r *http.Request) {
	var item map[string]any
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, "decode author: "+err.Error(), http.StatusBadRequest)
		return
	}
	id, err := storeCreateAuthor(r.Context(), item)
	if err != nil {
		http.Error(w, "create author: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(map[string]any{"id": id}); err != nil {
		http.Error(w, "encode author: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

func UpdateAuthor(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	var item map[string]any
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, "decode author: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := storeUpdateAuthor(r.Context(), id, item); err != nil {
		http.Error(w, "update author: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func DeleteAuthor(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	if err := storeDeleteAuthor(r.Context(), id); err != nil {
		http.Error(w, "delete author: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
