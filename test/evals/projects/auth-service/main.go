package main

import (
	"fmt"
	"net/http"

	"auth-service/middleware"
)

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("/public", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Public endpoint")
	})

	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Protected endpoint")
	})
	mux.Handle("/protected", middleware.AuthMiddleware(protected))

	fmt.Println("Server starting on :8080")
	http.ListenAndServe(":8080", mux)
}
