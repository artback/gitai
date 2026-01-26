package main

import "fmt"

// Add returns the sum of two integers
func Add(a, b int) int {
	return a + b
}

// Subtract returns the difference between two integers
func Subtract(a, b int) int {
	return a - b
}

func main() {
	fmt.Println("Calc App v2.0 - Advanced")
	fmt.Println("Result:", Add(2, 3))
}
