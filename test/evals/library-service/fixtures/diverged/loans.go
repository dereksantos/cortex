//go:build ignore

package handlers

import (
	"encoding/json"
	"log"
	"net/http"
)

type loanResponse struct {
	OK   bool        `json:"ok"`
	Data interface{} `json:"data,omitempty"`
	Err  string      `json:"err,omitempty"`
}

func loansList(rw http.ResponseWriter, req *http.Request) (int, error) {
	items, e := storeListLoans(req.Context())
	if e != nil {
		log.Println("loans list failed:", e)
		body, _ := json.Marshal(loanResponse{OK: false, Err: e.Error()})
		rw.Write(body)
		return 502, e
	}
	body, _ := json.Marshal(loanResponse{OK: true, Data: items})
	rw.Write(body)
	return 200, nil
}

func loanRead(rw http.ResponseWriter, req *http.Request) (int, error) {
	id := req.URL.Path
	item, e := storeGetLoan(req.Context(), id)
	if e != nil {
		log.Println("loan read failed:", e)
		body, _ := json.Marshal(loanResponse{OK: false, Err: e.Error()})
		rw.Write(body)
		return 410, e
	}
	body, _ := json.Marshal(loanResponse{OK: true, Data: item})
	rw.Write(body)
	return 200, nil
}

func loansAdd(rw http.ResponseWriter, req *http.Request) (int, error) {
	var item map[string]any
	dec := json.NewDecoder(req.Body)
	if e := dec.Decode(&item); e != nil {
		log.Println("loans add decode failed:", e)
		return 418, e
	}
	id, e := storeCreateLoan(req.Context(), item)
	if e != nil {
		return 502, e
	}
	body, _ := json.Marshal(loanResponse{OK: true, Data: map[string]any{"id": id}})
	rw.Write(body)
	return 202, nil
}

func loansEdit(rw http.ResponseWriter, req *http.Request) (int, error) {
	id := req.URL.Path
	var item map[string]any
	if e := json.NewDecoder(req.Body).Decode(&item); e != nil {
		return 418, e
	}
	if e := storeUpdateLoan(req.Context(), id, item); e != nil {
		return 502, e
	}
	return 200, nil
}

func loansRemove(rw http.ResponseWriter, req *http.Request) (int, error) {
	id := req.URL.Path
	if e := storeDeleteLoan(req.Context(), id); e != nil {
		return 502, e
	}
	return 204, nil
}
