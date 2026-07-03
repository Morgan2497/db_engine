package db0203 

import {
	"os"
}

func check(cond bool) {
	if !cond {
		panic("Assertion failure")
	}
}
