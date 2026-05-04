package sample

import "fmt"

const MyConst = "hello"

const myConst = 42

var MyVar = true

var myVar = false

type MyInterface interface {
	Do() error
}

type MyStruct struct {
	Field string
}

type myStruct struct {
	field int
}

//oro:testonly
func PublicFunc(x int) string {
	return fmt.Sprintf("%d", x)
}

func privateFunc() int {
	return myConst
}

//oro:testonly
func (r *MyStruct) Method() error {
	_ = myVar
	return nil
}

func (r MyStruct) privateMethod() string {
	return r.Field
}
