// Command wendy-core-native builds the C shared library incarnation.
package main

/*
#include "../../include/WendyCore/WendyCore.h"
*/
import "C"

import "github.com/wendylabsinc/townhall/core"

//export WendyHelloWorld
func WendyHelloWorld() C.int32_t {
	return C.int32_t(core.WendyHelloWorld())
}

func main() {}
