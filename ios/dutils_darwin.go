// Package ios is a collection of interfaces to the local storage subsystem;
// the package includes OS-dependent implementations for those interfaces.
/*
 * Copyright (c) 2019, NVIDIA CORPORATION. All rights reserved.
 */
package ios

// fs2disks is used when a mountpath is added to
// retrieve the disk(s) associated with a filesystem.
// This returns multiple disks only if the filesystem is RAID.
// TODO: implementation
func fs2disks(fs string) (disks fsDisks) {
	return
}
