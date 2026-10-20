package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Println("crew " + version)
		return
	}
	switch os.Args[1] {
	case "probe":
		os.Exit(cmdProbe())
	case "version":
		fmt.Println(version)
	default:
		os.Exit(dispatch(os.Args[1], os.Args[2:]))
	}
}
