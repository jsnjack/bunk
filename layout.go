// layout.go - Binary Space Partitioning (BSP) tree for pane layout.
//
// The screen is divided recursively using a BSP tree:
//
//	Node (internal):
//	  Owns a rectangular region (x, y, w, h).
//	  Divided into two children by a single split line.
//	  splitVertical   → left child | border | right child
//	  splitHorizontal → top child  / border / bottom child
//
//	Node (leaf):
//	  Owns a rectangular region (x, y, w, h).
//	  Contains exactly one Pane that fills that region.
//
// Layout math (border is always 1 cell wide/tall):
//
//	Vertical split of (x, y, w, h), half = w/2:
//	  left  child: (x,        y, half,      h)
//	  right child: (x+half+1, y, w-half-1,  h)
//	  border column: x + half
//
//	Horizontal split of (x, y, w, h), half = h/2:
//	  top    child: (x, y,        w, half)
//	  bottom child: (x, y+half+1, w, h-half-1)
//	  border row: y + half
//
// When a leaf is removed the sibling is promoted into the parent's slot and
// resized to fill the parent's full region.
package main

// splitDir describes how an internal node divides its region.
type splitDir int

const (
	splitVertical   splitDir = iota // children are side-by-side   (left | right)
	splitHorizontal                 // children are stacked top/bottom (top / bottom)
)

// Node is one element of the BSP tree.
// If pane != nil it is a leaf; otherwise left and right are set.
type Node struct {
	x, y, w, h int // bounding box this node owns (inclusive of border columns/rows)
	dir        splitDir

	// Leaf fields.
	pane *Pane

	// Internal node fields.
	left, right *Node
	parent      *Node
}

// newLeaf creates a leaf node wrapping pane p.
func newLeaf(p *Pane, x, y, w, h int) *Node {
	return &Node{x: x, y: y, w: w, h: h, pane: p}
}

// isLeaf returns true when this node holds a pane directly.
func (n *Node) isLeaf() bool { return n.pane != nil }

// ---------------------------------------------------------------------------
// Mutation: split
// ---------------------------------------------------------------------------

// split converts this leaf into an internal node.
//
// The existing pane is moved into the left/top child (resized to its new,
// smaller region).  newPane fills the right/bottom child.  After the call n is
// an internal node with two leaf children.
func (n *Node) split(newPane *Pane, d splitDir) {
	if !n.isLeaf() {
		return
	}

	// Calculate bounding boxes for the two children.
	var lx, ly, lw, lh, rx, ry, rw, rh int
	if d == splitVertical {
		half := n.w / 2
		lx, ly, lw, lh = n.x, n.y, half, n.h
		rx, ry, rw, rh = n.x+half+1, n.y, n.w-half-1, n.h
	} else {
		half := n.h / 2
		lx, ly, lw, lh = n.x, n.y, n.w, half
		rx, ry, rw, rh = n.x, n.y+half+1, n.w, n.h-half-1
	}

	existing := n.pane
	existing.resize(lx, ly, lw, lh) // shrink the existing pane to the left/top half

	// Build the two child nodes.
	left := &Node{x: lx, y: ly, w: lw, h: lh, pane: existing, parent: n}
	right := &Node{x: rx, y: ry, w: rw, h: rh, pane: newPane, parent: n}

	// Convert n from leaf → internal.
	n.pane = nil
	n.dir = d
	n.left = left
	n.right = right
}

// ---------------------------------------------------------------------------
// Query helpers
// ---------------------------------------------------------------------------

// findPane returns the leaf node whose pane == p, or nil if not found.
func (n *Node) findPane(p *Pane) *Node {
	if n.isLeaf() {
		if n.pane == p {
			return n
		}
		return nil
	}
	if found := n.left.findPane(p); found != nil {
		return found
	}
	return n.right.findPane(p)
}

// leaves returns all leaf nodes in depth-first order.
func (n *Node) leaves() []*Node {
	if n.isLeaf() {
		return []*Node{n}
	}
	return append(n.left.leaves(), n.right.leaves()...)
}

// ---------------------------------------------------------------------------
// Resize
// ---------------------------------------------------------------------------

// resize recalculates the layout for a new bounding box, propagating new
// dimensions down to every descendant pane.  The split ratio is always kept
// at 50/50 (re-evaluated from the new total size), which ensures panes are
// always usable after a terminal resize.
func (n *Node) resize(x, y, w, h int) {
	n.x, n.y, n.w, n.h = x, y, w, h

	if n.isLeaf() {
		n.pane.resize(x, y, w, h)
		return
	}

	if n.dir == splitVertical {
		half := w / 2
		n.left.resize(x, y, half, h)
		n.right.resize(x+half+1, y, w-half-1, h)
	} else {
		half := h / 2
		n.left.resize(x, y, w, half)
		n.right.resize(x, y+half+1, w, h-half-1)
	}
}

// ---------------------------------------------------------------------------
// Removal
// ---------------------------------------------------------------------------

// removeFromTree removes the leaf containing pane p from the tree rooted at
// root and returns the new root.
//
// The sibling of the removed leaf is promoted: it takes over the parent's full
// bounding box and is resized accordingly.  If the removed pane was the only
// one (root is a leaf), nil is returned.
func removeFromTree(root *Node, p *Pane) *Node {
	node := root.findPane(p)
	if node == nil {
		return root // p not in this tree
	}

	parent := node.parent
	if parent == nil {
		// p was the only pane (root leaf).
		return nil
	}

	// Identify the sibling that will survive.
	sibling := parent.right
	if parent.right == node {
		sibling = parent.left
	}

	// Promote sibling into parent's slot.
	sibling.parent = parent.parent
	sibling.resize(parent.x, parent.y, parent.w, parent.h)

	gp := parent.parent
	if gp == nil {
		// Parent was the root; sibling becomes the new root.
		return sibling
	}
	if gp.left == parent {
		gp.left = sibling
	} else {
		gp.right = sibling
	}
	return root
}
