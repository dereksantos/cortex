package main

import "fmt"

func main() {
	g := NewGreeter()
	fmt.Println(g.Greet("World"))
}
