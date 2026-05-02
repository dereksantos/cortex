//go:build ignore

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func ListMembers(w http.ResponseWriter, r *http.Request) {
	items, err := storeListMembers(r.Context())
	if err != nil {
		http.Error(w, fmt.Errorf("list members: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(items); err != nil {
		http.Error(w, fmt.Errorf("encode members: %w", err).Error(), http.StatusInternalServerError)
		return
	}
}

func GetMember(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	item, err := storeGetMember(r.Context(), id)
	if err != nil {
		http.Error(w, fmt.Errorf("get member: %w", err).Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(item); err != nil {
		http.Error(w, fmt.Errorf("encode member: %w", err).Error(), http.StatusInternalServerError)
		return
	}
}

func CreateMember(w http.ResponseWriter, r *http.Request) {
	var item map[string]any
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, fmt.Errorf("decode member: %w", err).Error(), http.StatusBadRequest)
		return
	}
	id, err := storeCreateMember(r.Context(), item)
	if err != nil {
		http.Error(w, fmt.Errorf("create member: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(map[string]any{"id": id}); err != nil {
		http.Error(w, fmt.Errorf("encode member: %w", err).Error(), http.StatusInternalServerError)
		return
	}
}

func UpdateMember(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	var item map[string]any
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, fmt.Errorf("decode member: %w", err).Error(), http.StatusBadRequest)
		return
	}
	if err := storeUpdateMember(r.Context(), id, item); err != nil {
		http.Error(w, fmt.Errorf("update member: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func DeleteMember(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	if err := storeDeleteMember(r.Context(), id); err != nil {
		http.Error(w, fmt.Errorf("delete member: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
