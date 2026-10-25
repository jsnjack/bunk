package main

import "C"

// Exported for the C code in shim.go and view.go. They take no GObject
// arguments, so cgo marshals nothing and nothing is allocated per call. A
// view reports its pane by id.

func paneByID(id C.int) *Pane {
	if int(id) < 0 || int(id) >= len(theApp.panes) {
		return nil
	}
	return theApp.panes[id]
}

//export goBeforePaint
func goBeforePaint() { theApp.beforePaint() }

//export goAfterPaint
func goAfterPaint() { theApp.afterPaint() }

//export goViewRender
func goViewRender(id, dw, dh C.int, scale C.double) C.int {
	if p := paneByID(id); p != nil && p.render(int(dw), int(dh), float64(scale)) {
		return 1
	}
	return 0
}

//export goViewRealized
func goViewRealized(id C.int) {
	if p := paneByID(id); p != nil {
		p.realized()
	}
}

//export goViewUnrealize
func goViewUnrealize(id C.int) {
	if p := paneByID(id); p != nil {
		p.unrealize()
		if p.removeOnce { // the tab was closed: forget the pane
			theApp.panes[id] = nil
		}
	}
}

//export goViewUnmapped
func goViewUnmapped(id C.int) {
	if p := paneByID(id); p != nil {
		p.unmapped()
	}
}
