package memforge

import (
	"fmt"
	"memcore"
	"unsafe"
)

const headerStoreInitialBytes = 64 * 1024

var (
	headerStoreRegionID uint32
	headerStoreBase     uintptr
	headerStoreCap      uint64
	headerStoreBump     uint64
	headerStoreMap      memcore.MemoryMap
)

func memforgeHeaderStoreEnsure() {
	if headerStoreCap != 0 {
		return
	}

	mmap, err := memcore.MemmapRequest(headerStoreInitialBytes, memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)
	if err != nil {
		panic(fmt.Errorf("memforge header store: mmap failed: %w", err))
	}

	headerStoreMap = mmap
	headerStoreBase = uintptr(unsafe.Pointer(&mmap[0]))
	headerStoreCap = uint64(headerStoreInitialBytes)
	headerStoreRegionID = memcore.MemcoreRegionRegister(headerStoreBase, headerStoreCap)
}

func memforgeHeaderStoreGrow(neededEnd uint64) {
	if neededEnd <= headerStoreCap {
		return
	}

	newCap := headerStoreCap * 2
	if newCap < neededEnd {
		newCap = neededEnd
	}

	newMap, err := memcore.MemmapRemapAt(
		unsafe.Pointer(headerStoreBase),
		int(headerStoreCap),
		int(newCap),
		memcore.MREMAP_MAYMOVE,
	)
	if err != nil {
		panic(fmt.Errorf("memforge header store: grow failed: %w", err))
	}

	headerStoreMap = newMap
	headerStoreBase = uintptr(unsafe.Pointer(&newMap[0]))
	headerStoreCap = uint64(newCap)
	memcore.MemcoreRegionBaseUpdate(headerStoreRegionID, headerStoreBase)
}

func memforgeHeaderAllocate(sizeBytes, alignment uint64) memcore.MarkRaw {
	memforgeHeaderStoreEnsure()

	alignedIdx := alignIdxUp(headerStoreBump, alignment)
	neededEnd := alignedIdx + sizeBytes
	if neededEnd > headerStoreCap {
		memforgeHeaderStoreGrow(neededEnd)
	}

	mark := memcore.MemcoreMarkCreate(headerStoreRegionID, uintptr(alignedIdx))
	headerStoreBump = neededEnd
	return mark
}

func memforgeDataRegionCreate(sizeBytes uint64) (regionID uint32, mmapBase uintptr, mmap memcore.MemoryMap) {
	mmap, err := memcore.MemmapRequest(int(sizeBytes), memcore.PROT_READWRITE, memcore.MAP_ANON_PRIVATE)
	if err != nil {
		panic(fmt.Errorf("memforge data region: mmap failed: %w", err))
	}

	mmapBase = uintptr(unsafe.Pointer(&mmap[0]))
	regionID = memcore.MemcoreRegionRegister(mmapBase, sizeBytes)
	return regionID, mmapBase, mmap
}

func memforgeDataRegionDestroy(regionID uint32, mmapBase uintptr, sizeBytes uint64) {
	memcore.MemcoreRegionUnregister(regionID)
	if mmapBase != 0 && sizeBytes != 0 {
		if err := memcore.MemmapUnmapAt(unsafe.Pointer(mmapBase), int(sizeBytes)); err != nil {
			panic(fmt.Errorf("memforge data region: unmap failed: %w", err))
		}
	}
}
