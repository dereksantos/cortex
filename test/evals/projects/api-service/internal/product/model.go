package product

// Product represents a product in the catalog.
type Product struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
	Inventory   int     `json:"inventory"`
}
