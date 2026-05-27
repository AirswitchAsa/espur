// espur-genkey prints a fresh ESPUR_MASTER_KEY value to stdout. Useful for
// the one-time deploy bootstrap step. Spec: secrets.dog.md.
package main

import (
	"fmt"

	"github.com/punny/espur/internal/secrets"
)

func main() {
	k, err := secrets.GenerateIdentity()
	if err != nil {
		panic(err)
	}
	fmt.Println(k)
}
