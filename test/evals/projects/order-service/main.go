package main

import (
	"fmt"
	"order-service/order"
)

func main() {
	o := &order.Order{
		ID:       "ORD-001",
		Customer: "Alice",
		Items:    []string{"Book", "Pen"},
		Total:    24.99,
	}

	if err := order.ProcessOrder(o); err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	fmt.Println("Order processed successfully")
}
