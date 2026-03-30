package memforge

import (
	"fmt"
	"memcore"
)

type DynamicLinearAllocatorGrowthMaxViolation struct {
	MaxCapacity uint64
	Required    uint64
	Proposed    uint64
}

func (e DynamicLinearAllocatorGrowthMaxViolation) Error() string {
	return fmt.Sprintf(
		"dynamic linear allocator growth max violation: proposed %d (required %d) exceeds max %d",
		e.Proposed, e.Required, e.MaxCapacity,
	)
}

/*
DynamicLinearAllocatorGrowthDoubleOrNeeded grows capacity by doubling, unless the
required capacity is larger than doubled capacity.

It returns max(currentCap*2, neededCap), while handling 0-capacity starts and
integer overflow saturation.
*/
func DynamicLinearAllocatorGrowthDoubleOrNeeded(currentCap, neededCap uint64) uint64 {
	if currentCap == 0 {
		return neededCap
	}

	doubledCap := dynamicLinearAllocatorCapacityDoubleSaturated(currentCap)
	if doubledCap < neededCap {
		return neededCap
	}

	return doubledCap
}

/*
DynamicLinearAllocatorGrowthTemplateDoubleOrNeededWithMaxPanic creates a growth
strategy that uses DynamicLinearAllocatorGrowthDoubleOrNeeded and enforces a
hard max capacity.

The returned strategy panics with DynamicLinearAllocatorGrowthMaxViolation when
the computed capacity exceeds maxCapacityBytes.
*/
func DynamicLinearAllocatorGrowthTemplateDoubleOrNeededWithMaxPanic(maxCapacityBytes uint64) GrowthStrategy {
	return func(currentCap, neededCap uint64) uint64 {
		proposedCap := DynamicLinearAllocatorGrowthDoubleOrNeeded(currentCap, neededCap)
		if proposedCap > maxCapacityBytes {
			panic(DynamicLinearAllocatorGrowthMaxViolation{
				MaxCapacity: maxCapacityBytes,
				Required:    neededCap,
				Proposed:    proposedCap,
			})
		}

		return proposedCap
	}
}

/*
DynamicLinearAllocatorGrowthTemplateDoubleOrNeededWithMaxPanicID registers a
max-limited growth strategy and returns its memcore function ID.
*/
func DynamicLinearAllocatorGrowthTemplateDoubleOrNeededWithMaxPanicID(maxCapacityBytes uint64) memcore.FunctionID {
	strategy := DynamicLinearAllocatorGrowthTemplateDoubleOrNeededWithMaxPanic(maxCapacityBytes)
	return memcore.MemcoreFunctionRegisterTyped[GrowthStrategy](strategy)
}

//go:inline
func dynamicLinearAllocatorCapacityDoubleSaturated(currentCap uint64) uint64 {
	if currentCap > (^uint64(0))/2 {
		return ^uint64(0)
	}

	return currentCap * 2
}
