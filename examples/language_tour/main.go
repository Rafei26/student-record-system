// language_tour prints short, runnable demonstrations of the Go features
// discussed in Part A of the report. Run it with:
//
//	go run ./examples/language_tour
package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ---- 1. Types, naming and constants ---------------------------------------

type Weekday int // named type + iota gives an "enum"

const (
	Mon Weekday = iota
	Tue
	Wed
)

func types() {
	var count int    // zero value is 0: no uninitialised variables in Go
	var name string  // zero value is ""
	var ok bool      // zero value is false
	ratio := 7 / 2   // integer division -> 3
	exact := 7 / 2.0 // untyped constant becomes float64 -> 3.5
	var small uint8 = 255
	small++         // wraps silently to 0 (no overflow error)
	word := "héllo" // UTF-8 text
	fmt.Println("zero values:", count, name == "", ok)
	fmt.Println("7/2 =", ratio, "| 7/2.0 =", exact, "| uint8 255+1 =", small)
	fmt.Println("len(\"héllo\") bytes:", len(word), "| runes:", len([]rune(word)))
	fmt.Println("iota:", Mon, Tue, Wed)

	// Arrays copy by value; slices share one backing array.
	arr := [3]int{1, 2, 3}
	copyArr := arr
	copyArr[0] = 99
	s1 := []int{1, 2, 3}
	s2 := s1
	s2[0] = 99
	fmt.Println("array original:", arr, "| slice original after edit via alias:", s1)
}

// ---- 2. Control structures -------------------------------------------------

func control() {
	if n := 42; n%2 == 0 { // statement before the condition; n is scoped to the if
		fmt.Println("even:", n)
	}
	for i := range 3 { // Go 1.22: range over an integer
		fmt.Print("i=", i, " ")
	}
	fmt.Println()
	switch grade := 74; { // no fallthrough by default; tagless switch
	case grade >= 80:
		fmt.Println("HD")
	case grade >= 70:
		fmt.Println("D")
	default:
		fmt.Println("lower")
	}
	for _, v := range []any{1, "go", 2.5, nil} { // type switch
		switch x := v.(type) {
		case int:
			fmt.Print("int:", x+1, " ")
		case string:
			fmt.Print("string:", len(x), " ")
		case nil:
			fmt.Print("nil ")
		default:
			fmt.Printf("%T ", x)
		}
	}
	fmt.Println()
outer:
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			if i*j == 2 {
				fmt.Println("labelled break at", i, j)
				break outer
			}
		}
	}
	for i := range 3 {
		defer fmt.Print("defer", i, " ") // runs last-in, first-out when control() returns
	}
}

// ---- 3. Subprograms --------------------------------------------------------

func divmod(a, b int) (quotient, remainder int) { return a / b, a % b } // multiple results
func sum(nums ...int) (t int) { // variadic
	for _, n := range nums {
		t += n
	}
	return
}
func counter() func() int { // closure captures c
	c := 0
	return func() int { c++; return c }
}

type Celsius float64

func (c Celsius) Fahrenheit() float64 { return float64(c)*9/5 + 32 } // method on a named type

func subprograms() {
	q, r := divmod(17, 5)
	next := counter()
	next()
	fmt.Println("divmod:", q, r, "| sum:", sum(1, 2, 3), "| closure:", next(), "| method:", Celsius(100).Fahrenheit())
}

// ---- 4. Generics -----------------------------------------------------------

type Number interface{ ~int | ~float64 }

func Sum[T Number](xs []T) (t T) {
	for _, x := range xs {
		t += x
	}
	return
}

func Map[T, U any](xs []T, f func(T) U) []U {
	out := make([]U, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

func generics() {
	fmt.Println("Sum ints:", Sum([]int{1, 2, 3}), "| Sum floats:", Sum([]float64{1.5, 2.5}),
		"| Map:", Map([]int{1, 2, 3}, func(n int) string { return fmt.Sprint("#", n) }))
}

// ---- 5. Types, interfaces and embedding (no inheritance) -----------------------

type Person struct{ Name string }

func (p Person) Describe() string { return "person " + p.Name }

type Student struct {
	Person // embedded: Student gains Person's fields and methods
	ID     string
}

type Describer interface{ Describe() string } // satisfied implicitly

func embedding() {
	var d Describer = Student{Person{"Aisha"}, "10000001"}
	fmt.Println("promoted method via interface:", d.Describe())
}

// ---- 6. Errors, defer, panic and recover -----------------------------------------

var ErrEmpty = errors.New("empty input")

type RangeError struct{ Got int }

func (e RangeError) Error() string { return fmt.Sprintf("mark %d out of range", e.Got) }

func check(mark int) error {
	if mark < 0 || mark > 100 {
		return fmt.Errorf("check: %w", RangeError{mark}) // wrap with context
	}
	return nil
}

func safeDivide(a, b int) (result int, err error) {
	defer func() {
		if r := recover(); r != nil { // convert a panic into an ordinary error
			err = fmt.Errorf("recovered: %v", r)
		}
	}()
	return a / b, nil // integer divide by zero panics at run time
}

func errorsDemo() {
	err := check(120)
	var re RangeError
	fmt.Println("wrapped:", err, "| errors.As:", errors.As(err, &re), re.Got, "| Is(ErrEmpty):", errors.Is(err, ErrEmpty))
	_, err = safeDivide(1, 0)
	fmt.Println("panic->error:", err)
}

// ---- 7. Concurrency ----------------------------------------------------------

func concurrency() {
	// Worker pool: 3 goroutines read jobs from one channel, send results on another.
	jobs, results := make(chan int), make(chan int)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				results <- n * n
			}
		}()
	}
	go func() {
		for n := 1; n <= 6; n++ {
			jobs <- n
		}
		close(jobs)
	}()
	go func() { wg.Wait(); close(results) }()
	var squares []int
	for r := range results {
		squares = append(squares, r)
	}
	sort.Ints(squares) // goroutine order is not deterministic, so sort before printing
	fmt.Println("worker pool squares:", squares)

	// Mutex-protected shared counter.
	var mu sync.Mutex
	total := 0
	var wg2 sync.WaitGroup
	for range 1000 {
		wg2.Add(1)
		go func() { defer wg2.Done(); mu.Lock(); total++; mu.Unlock() }()
	}
	wg2.Wait()
	fmt.Println("1000 goroutines incremented a counter to:", total)

	// select with a timeout, driven by a context (Go's event/cancellation mechanism).
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	slow := make(chan string)
	select {
	case msg := <-slow:
		fmt.Println(msg)
	case <-ctx.Done():
		fmt.Println("select: gave up waiting:", ctx.Err())
	}
}

func main() {
	for _, section := range []struct {
		title string
		run   func()
	}{
		{"1. Types, naming, constants", types}, {"2. Control structures", control},
		{"3. Subprograms", subprograms}, {"4. Generics", generics},
		{"5. Embedding and interfaces", embedding}, {"6. Errors, defer, panic, recover", errorsDemo},
		{"7. Concurrency", concurrency},
	} {
		fmt.Printf("\n== %s ==\n", section.title)
		section.run()
	}
	fmt.Println()
}
