//go:build ignore

package handlers

// Identical to cohesive/books.go — this is the S1 baseline. The drift in this
// fixture lives in the other 4 resources, one realistic small-model slip each.

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func ListBooks(w http.ResponseWriter, r *http.Request) {
	items, err := storeListBooks(r.Context())
	if err != nil {
		http.Error(w, fmt.Errorf("list books: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(items); err != nil {
		http.Error(w, fmt.Errorf("encode books: %w", err).Error(), http.StatusInternalServerError)
		return
	}
}

func GetBook(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	item, err := storeGetBook(r.Context(), id)
	if err != nil {
		http.Error(w, fmt.Errorf("get book: %w", err).Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(item); err != nil {
		http.Error(w, fmt.Errorf("encode book: %w", err).Error(), http.StatusInternalServerError)
		return
	}
}

func CreateBook(w http.ResponseWriter, r *http.Request) {
	var item map[string]any
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, fmt.Errorf("decode book: %w", err).Error(), http.StatusBadRequest)
		return
	}
	id, err := storeCreateBook(r.Context(), item)
	if err != nil {
		http.Error(w, fmt.Errorf("create book: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(map[string]any{"id": id}); err != nil {
		http.Error(w, fmt.Errorf("encode book: %w", err).Error(), http.StatusInternalServerError)
		return
	}
}

func UpdateBook(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	var item map[string]any
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, fmt.Errorf("decode book: %w", err).Error(), http.StatusBadRequest)
		return
	}
	if err := storeUpdateBook(r.Context(), id, item); err != nil {
		http.Error(w, fmt.Errorf("update book: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func DeleteBook(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	if err := storeDeleteBook(r.Context(), id); err != nil {
		http.Error(w, fmt.Errorf("delete book: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
