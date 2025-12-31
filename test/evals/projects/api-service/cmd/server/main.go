package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/example/api-service/internal/auth"
	"github.com/example/api-service/internal/order"
	"github.com/example/api-service/internal/product"
)

func main() {
	// Initialize services
	productSvc := product.NewProductService()
	orderSvc := order.NewOrderService()

	// Setup routes
	mux := http.NewServeMux()

	// Product endpoints
	mux.HandleFunc("/products/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/products/"):]
		p, err := productSvc.GetProduct(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"id":"%s","name":"%s","price":%v}`, p.ID, p.Name, p.Price)
	})

	// Order endpoints
	mux.HandleFunc("/orders", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Simplified order creation
		o := &order.Order{ID: "ord-1", CustomerID: "cust-1", Status: order.StatusPending}
		if err := orderSvc.CreateOrder(r.Context(), o); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":"%s","status":"%s"}`, o.ID, o.Status)
	})

	// Wrap with auth middleware
	handler := auth.JWTMiddleware(mux)

	addr := ":8080"
	log.Printf("Starting server on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
	}
}
