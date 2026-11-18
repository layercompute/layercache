package artifact

// SetFilesystemSpaceForTest replaces the store's filesystem probe. Call it
// only while no store operations are running.
func SetFilesystemSpaceForTest(store *Store, available, total, allocationUnit int64) {
	store.probeSpace = func(string) (filesystemSpace, error) {
		return filesystemSpace{availableBytes: available, totalBytes: total, allocationUnit: allocationUnit}, nil
	}
}
