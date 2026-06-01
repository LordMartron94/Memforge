package memforge

import (
	"fmt"
	"memcore"
)

/*
DynamicLinearAllocatorGrowthMaxViolation reports a capped growth strategy exceeding maxCapacityBytes.
*/
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
DynamicLinearAllocatorGrowthDoubleOrNeeded doubles arena capacity unless neededCap is larger.

[Context]
Suitable default growth for DynamicLinearAllocator. When currentCap is zero, returns neededCap.
Doubling saturates at max uint64 instead of wrapping.

[Parameters]
currentCap - Current arena data capacity.
neededCap - Minimum capacity required for the pending allocation.

[Returns]
max(currentCap*2, neededCap) with saturation when doubling overflows.

[Complexity]
Time: O(1). Space: O(1).

[Side Effects]
Pure function.
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
DynamicLinearAllocatorGrowthTemplateDoubleOrNeededWithMaxPanic wraps double-or-needed growth with a hard cap.

[Parameters]
maxCapacityBytes - Maximum allowed arena data capacity.

[Returns]
A GrowthStrategy suitable for DynamicLinearAllocatorCreateFunction.

[Errors]
Panics with DynamicLinearAllocatorGrowthMaxViolation when proposed capacity exceeds maxCapacityBytes.

[Side Effects]
Pure factory; the returned closure panics on violation during growth.
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
DynamicLinearAllocatorGrowthTemplateDoubleOrNeededWithMaxPanicID registers the max-limited growth strategy.

[Returns]
A memcore.FunctionID for use with DynamicLinearAllocatorCreate.

[Side Effects]
Registers the strategy in memcore's function table.
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
