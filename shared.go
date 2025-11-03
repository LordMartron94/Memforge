package memforge

import "memcore"

const allocatorDataAddrAlignment = 64 * memcore.Byte

//go:inline
func capacityGuarantee(alignedIdx, cap, requestAmountBytes uint64) bool {
	return alignedIdx+requestAmountBytes <= cap
}

//go:inline
func alignmentValidate(alignment uint64) {
	if alignment <= 0 || (alignment&(alignment-1)) != 0 {
		panic("alignment must be a power of two and > 0")
	}
}

//go:inline
func alignIdxUp(idx uint64, alignment uint64) uint64 {
	mask := alignment - 1
	return (idx + mask) &^ mask
}
