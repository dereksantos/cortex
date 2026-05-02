//go:build ignore

package handlers

// Drift: response writes use json.Marshal + w.Write instead of
// json.NewEncoder(w).Encode. Error wrapping, naming, signatures, status
// codes, and validation match S1.

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func ListLoans(w http.ResponseWriter, r *http.Request) {
	items, err := storeListLoans(r.Context())
	if err != nil {
		http.Error(w, fmt.Errorf("list loans: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	body, err := json.Marshal(items)
	if err != nil {
		http.Error(w, fmt.Errorf("encode loans: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.Write(body)
}

func GetLoan(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	item, err := storeGetLoan(r.Context(), id)
	if err != nil {
		http.Error(w, fmt.Errorf("get loan: %w", err).Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	body, err := json.Marshal(item)
	if err != nil {
		http.Error(w, fmt.Errorf("encode loan: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.Write(body)
}

func CreateLoan(w http.ResponseWriter, r *http.Request) {
	var item map[string]any
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, fmt.Errorf("decode loan: %w", err).Error(), http.StatusBadRequest)
		return
	}
	id, err := storeCreateLoan(r.Context(), item)
	if err != nil {
		http.Error(w, fmt.Errorf("create loan: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	body, err := json.Marshal(map[string]any{"id": id})
	if err != nil {
		http.Error(w, fmt.Errorf("encode loan: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.Write(body)
}

func UpdateLoan(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	var item map[string]any
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, fmt.Errorf("decode loan: %w", err).Error(), http.StatusBadRequest)
		return
	}
	if err := storeUpdateLoan(r.Context(), id, item); err != nil {
		http.Error(w, fmt.Errorf("update loan: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func DeleteLoan(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, fmt.Errorf("missing id").Error(), http.StatusBadRequest)
		return
	}
	if err := storeDeleteLoan(r.Context(), id); err != nil {
		http.Error(w, fmt.Errorf("delete loan: %w", err).Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
