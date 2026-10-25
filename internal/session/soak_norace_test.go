//go:build !race

package session

// soakCheckBytes is the output soak's size without SOAK=1.
const soakCheckBytes = 20 << 20
