// cmd/procula/main.go is the binary entry point for the procula service.
// All application logic lives in the procula package (the module root);
// this file only calls Run() so the module can also be used as a library.
package main

import procula "procula"

func main() {
	procula.Run()
}
