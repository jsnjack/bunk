package main

import "C"

// Exported for the C code in shim.go and view.go. They take no GObject
// arguments, so cgo marshals nothing and nothing is allocated per call.

//export goBeforePaint
func goBeforePaint() { theApp.beforePaint() }

//export goAfterPaint
func goAfterPaint() { theApp.afterPaint() }

//export goViewRender
func goViewRender(dw, dh C.int, scale C.double) C.int {
	if theApp.viewRender(int(dw), int(dh), float64(scale)) {
		return 1
	}
	return 0
}

//export goViewRealized
func goViewRealized() { theApp.viewRealized() }
