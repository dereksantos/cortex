package main

import (
	"log"
	"net/http"
	"os"

	handlers "example.com/library"
)

// withPathID copies the URL path's {id} value into r.URL.Query() so the
// query-style handlers (which read r.URL.Query().Get("id")) work behind a
// /resource/{id} route.
func withPathID(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id != "" {
			q := r.URL.Query()
			q.Set("id", id)
			r.URL.RawQuery = q.Encode()
		}
		next(w, r)
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /books", handlers.ListBooks)
	mux.HandleFunc("POST /books", handlers.CreateBook)
	mux.HandleFunc("GET /books/{id}", withPathID(handlers.GetBook))
	mux.HandleFunc("PUT /books/{id}", withPathID(handlers.UpdateBook))
	mux.HandleFunc("DELETE /books/{id}", withPathID(handlers.DeleteBook))

	mux.HandleFunc("GET /authors", handlers.ListAuthors)
	mux.HandleFunc("POST /authors", handlers.CreateAuthor)
	mux.HandleFunc("GET /authors/{id}", withPathID(handlers.GetAuthor))
	mux.HandleFunc("PUT /authors/{id}", withPathID(handlers.UpdateAuthor))
	mux.HandleFunc("DELETE /authors/{id}", withPathID(handlers.DeleteAuthor))

	mux.HandleFunc("GET /loans", handlers.ListLoans)
	mux.HandleFunc("POST /loans", handlers.CreateLoan)
	mux.HandleFunc("GET /loans/{id}", withPathID(handlers.GetLoan))
	mux.HandleFunc("PUT /loans/{id}", withPathID(handlers.UpdateLoan))
	mux.HandleFunc("DELETE /loans/{id}", withPathID(handlers.DeleteLoan))

	mux.HandleFunc("GET /members", handlers.ListMembers)
	mux.HandleFunc("POST /members", handlers.CreateMember)
	mux.HandleFunc("GET /members/{id}", withPathID(handlers.GetMember))
	mux.HandleFunc("PUT /members/{id}", withPathID(handlers.UpdateMember))
	mux.HandleFunc("DELETE /members/{id}", withPathID(handlers.DeleteMember))

	mux.HandleFunc("GET /branches", handlers.ListBranches)
	mux.HandleFunc("POST /branches", handlers.CreateBranch)
	mux.HandleFunc("GET /branches/{id}", withPathID(handlers.GetBranch))
	mux.HandleFunc("PUT /branches/{id}", withPathID(handlers.UpdateBranch))
	mux.HandleFunc("DELETE /branches/{id}", withPathID(handlers.DeleteBranch))

	addr := ":" + port
	log.Printf("library-service fixture listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
