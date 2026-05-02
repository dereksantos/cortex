//go:build ignore

package handlers

// No drift in the handler — identical to cohesive/branches.go. The drift for
// this resource lives in branches_test.go (sequential, not table-driven).

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func ListBranches(w http.ResponseWriter, r *http.Request) {
	items, err := storeListBranches(r.Context())
	if err != nil {
		http.Error(w, fmt.Errorf("list branches: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(items); err != nil {
		http.Error(w, fmt.Errorf("encode branches: %w", err).Error(), http.StatusInternalServerError)
		return
	}
}

func GetBranch(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	item, err := storeGetBranch(r.Context(), id)
	if err != nil {
		http.Error(w, fmt.Errorf("get branch: %w", err).Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(item); err != nil {
		http.Error(w, fmt.Errorf("encode branch: %w", err).Error(), http.StatusInternalServerError)
		return
	}
}

func CreateBranch(w http.ResponseWriter, r *http.Request) {
	var item map[string]any
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, fmt.Errorf("decode branch: %w", err).Error(), http.StatusBadRequest)
		return
	}
	id, err := storeCreateBranch(r.Context(), item)
	if err != nil {
		http.Error(w, fmt.Errorf("create branch: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(map[string]any{"id": id}); err != nil {
		http.Error(w, fmt.Errorf("encode branch: %w", err).Error(), http.StatusInternalServerError)
		return
	}
}

func UpdateBranch(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	var item map[string]any
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, fmt.Errorf("decode branch: %w", err).Error(), http.StatusBadRequest)
		return
	}
	if err := storeUpdateBranch(r.Context(), id, item); err != nil {
		http.Error(w, fmt.Errorf("update branch: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func DeleteBranch(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	if err := storeDeleteBranch(r.Context(), id); err != nil {
		http.Error(w, fmt.Errorf("delete branch: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
