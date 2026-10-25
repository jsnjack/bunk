//go:build race

package session

// soakCheckBytes is the output soak's size without SOAK=1. The race
// detector slows the parser about tenfold, so the race build streams less.
const soakCheckBytes = 3 << 20
