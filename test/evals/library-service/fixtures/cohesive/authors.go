package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func ListAuthors(w http.ResponseWriter, r *http.Request) {
	items, err := storeListAuthors(r.Context())
	if err != nil {
		http.Error(w, fmt.Errorf("list authors: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(items); err != nil {
		http.Error(w, fmt.Errorf("encode authors: %w", err).Error(), http.StatusInternalServerError)
		return
	}
}

func GetAuthor(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	item, err := storeGetAuthor(r.Context(), id)
	if err != nil {
		http.Error(w, fmt.Errorf("get author: %w", err).Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(item); err != nil {
		http.Error(w, fmt.Errorf("encode author: %w", err).Error(), http.StatusInternalServerError)
		return
	}
}

func CreateAuthor(w http.ResponseWriter, r *http.Request) {
	var item map[string]any
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, fmt.Errorf("decode author: %w", err).Error(), http.StatusBadRequest)
		return
	}
	id, err := storeCreateAuthor(r.Context(), item)
	if err != nil {
		http.Error(w, fmt.Errorf("create author: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(map[string]any{"id": id}); err != nil {
		http.Error(w, fmt.Errorf("encode author: %w", err).Error(), http.StatusInternalServerError)
		return
	}
}

func UpdateAuthor(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	var item map[string]any
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, fmt.Errorf("decode author: %w", err).Error(), http.StatusBadRequest)
		return
	}
	if err := storeUpdateAuthor(r.Context(), id, item); err != nil {
		http.Error(w, fmt.Errorf("update author: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func DeleteAuthor(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	if err := storeDeleteAuthor(r.Context(), id); err != nil {
		http.Error(w, fmt.Errorf("delete author: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
