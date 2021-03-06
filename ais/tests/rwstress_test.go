// Package integration contains AIS integration tests.
/*
 * Copyright (c) 2018, NVIDIA CORPORATION. All rights reserved.
 */
package integration

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/aistore/3rdparty/atomic"
	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/tutils"
	"github.com/NVIDIA/aistore/tutils/readers"
	"github.com/NVIDIA/aistore/tutils/tassert"
)

//
// Example run:
//     go test -v -run=rwstress -args -numfiles=10 -cycles=10 -nodel -numops=5
//

const (
	rwdir    = "rwstress"
	fileSize = 1024 * 32 // file size

	// time to sleep if there is no object created yet in milliseconds
	// del time is slightly greater than get one to allow get work faster
	//   than del, so get won't miss objects because del run before get
	getSleep = 5
	delSleep = 10

	rwFileCreated = true
	rwFileExists  = true
	rwFileDeleted = false
	rwRunNormal   = false
	rwRunCleanUp  = true
)

type fileLock struct {
	locked bool
	exists bool
}
type fileLocks struct {
	mtx   sync.Mutex
	files []fileLock
}

var (
	fileNames []string
	filelock  fileLocks

	numLoops   int
	numFiles   int
	putCounter atomic.Int64
	getCounter atomic.Int64
	delCounter atomic.Int64
)

func tryLockFile(idx int) bool {
	filelock.mtx.Lock()
	defer filelock.mtx.Unlock()

	info := filelock.files[idx]
	if info.locked {
		return false
	}

	filelock.files[idx].locked = true
	return true
}

// tryLockNextAvailFile looks for an unlocked file that exists. If such file
// found it returns the id of the file and true. Returns 0 and false otherwise.
// idx is the preferred file id - a starting point to look for a file
func tryLockNextAvailFile(idx int) (int, bool) {
	filelock.mtx.Lock()
	defer filelock.mtx.Unlock()

	info := filelock.files[idx]
	if !info.locked && info.exists {
		filelock.files[idx].locked = true
		return idx, true
	}

	nextIdx := idx + 1
	for nextIdx != idx {
		if nextIdx >= len(fileNames) {
			nextIdx = 0
			continue
		}

		info = filelock.files[nextIdx]
		if !info.locked && info.exists {
			filelock.files[nextIdx].locked = true
			return nextIdx, true
		}

		nextIdx++
	}

	return 0, false
}

// unlockFile unlocks the file and marks if the file exists or not
func unlockFile(idx int, fileExists bool) {
	filelock.mtx.Lock()
	defer filelock.mtx.Unlock()

	filelock.files[idx].locked = false
	filelock.files[idx].exists = fileExists
}

// generates a list of random file names and a buffer to keep random data for filling up files
func generateRandomData(fileCount int) {
	fileNames = make([]string, fileCount)
	for i := 0; i < fileCount; i++ {
		fileNames[i] = tutils.GenRandomString(fnlen)
	}
}

// rwCanRunAsync limits the number of extra goroutines simultaneously
// running. '+1' is used to take into account the main thread, so if numops
// equals 1 then all operations run one by one non-concurrently
func rwCanRunAsync(currAsyncOps int64, maxAsycOps int) bool {
	return currAsyncOps+1 < int64(maxAsycOps)
}

func rwPutLoop(t *testing.T, proxyURL string, bck cmn.Bck, fileNames []string,
	taskGrp *sync.WaitGroup, cksumType string, doneCh chan int) {
	var (
		totalOps   int
		prc        int
		baseParams = tutils.BaseAPIParams(proxyURL)
	)
	errCh := make(chan error, numops)
	fileCount := len(fileNames)
	if taskGrp != nil {
		defer taskGrp.Done()
	}

	var wg sync.WaitGroup
	totalCount := fileCount * numLoops
	filesPut := 0
	for i := 0; i < numLoops; i++ {
		for idx := 0; idx < fileCount; idx++ {
			objName := fmt.Sprintf("%s/%s", rwdir, fileNames[idx])

			if ok := tryLockFile(idx); ok {
				// NOTE: This test depends on the files it creates, so ignore reader type, always use file reader
				r, err := readers.NewFileReader(baseDir, objName, fileSize, cksumType)
				if err != nil {
					t.Error(err)
					tassert.CheckFatal(t, r.Close())
					unlockFile(idx, rwFileCreated)
					return
				}

				n := putCounter.Load()
				if rwCanRunAsync(n, numops) {
					putCounter.Inc()
					wg.Add(1)
					go func(idx int) {
						tutils.PutAsync(&wg, proxyURL, bck, objName, r, errCh)
						unlockFile(idx, rwFileCreated)
						putCounter.Dec()
					}(idx)
				} else {
					putArgs := api.PutObjectArgs{
						BaseParams: baseParams,
						Bck:        bck,
						Object:     objName,
						Cksum:      r.Cksum(),
						Reader:     r,
					}
					err = api.PutObject(putArgs)
					if err != nil {
						errCh <- err
					}
					unlockFile(idx, rwFileCreated)
				}
				totalOps++
			}
			filesPut++
			newPrc := 100 * filesPut / totalCount
			if prc != newPrc {
				prc = newPrc
			}
			select {
			case e := <-errCh:
				tutils.Logf("PUT failed: %v\n", e.Error())
				t.Fail()
			default:
			}
		}
	}

	wg.Wait()

	// emit signals for DEL and GET loops
	doneCh <- 1
	if !skipdel {
		doneCh <- 1
	}
}

func rwDelLoop(t *testing.T, proxyURL string, bck cmn.Bck, fileNames []string, taskGrp *sync.WaitGroup, doneCh chan int, doCleanUp bool) {
	var (
		done              bool
		totalOps, currIdx int
		errCh             = make(chan error, 10)
		wg                = &sync.WaitGroup{}
	)

	if taskGrp != nil {
		defer taskGrp.Done()
	}

	for !done {
		if idx, ok := tryLockNextAvailFile(currIdx); ok {
			keyname := fmt.Sprintf("%s/%s", rwdir, fileNames[idx])
			n := delCounter.Load()
			if rwCanRunAsync(n, numops) {
				delCounter.Inc()
				wg.Add(1)
				localIdx := idx
				go func() {
					tutils.Del(proxyURL, bck, keyname, wg, errCh, true)
					unlockFile(localIdx, rwFileDeleted)
					delCounter.Dec()
				}()
			} else {
				tutils.Del(proxyURL, bck, keyname, nil, errCh, true)
				unlockFile(idx, rwFileDeleted)
			}

			currIdx = idx + 1
			if currIdx >= len(fileNames) {
				currIdx = 0
			}
			totalOps++
		} else {
			if doCleanUp {
				break
			}
			time.Sleep(delSleep * time.Millisecond)
		}

		select {
		case <-doneCh:
			done = true
		case e := <-errCh:
			tutils.Logf("DEL failed: %v\n", e.Error())
			t.Fail()
		default:
		}
	}
	wg.Wait()
}

func rwGetLoop(t *testing.T, proxyURL string, bck cmn.Bck, fileNames []string, taskGrp *sync.WaitGroup, doneCh chan int) {
	var (
		done              bool
		currIdx, totalOps int
		errCh             = make(chan error, 10)
		wg                = &sync.WaitGroup{}
		baseParams        = tutils.BaseAPIParams(proxyURL)
	)

	if taskGrp != nil {
		defer taskGrp.Done()
	}

	for !done {
		if idx, ok := tryLockNextAvailFile(currIdx); ok {
			keyname := fmt.Sprintf("%s/%s", rwdir, fileNames[idx])
			n := getCounter.Load()
			if rwCanRunAsync(n, numops) {
				getCounter.Inc()
				wg.Add(1)
				localIdx := idx
				go func() {
					defer wg.Done()

					_, err := api.GetObject(baseParams, bck, keyname)
					if err != nil {
						errCh <- err
					}
					unlockFile(localIdx, rwFileExists)
					getCounter.Dec()
				}()
			} else {
				_, err := api.GetObject(baseParams, bck, keyname)
				if err != nil {
					errCh <- err
				}
				unlockFile(idx, rwFileExists)
			}
			currIdx = idx + 1
			if currIdx >= len(fileNames) {
				currIdx = 0
			}
			totalOps++
		} else {
			time.Sleep(getSleep * time.Millisecond)
		}

		select {
		case <-doneCh:
			done = true
		case e := <-errCh:
			tutils.Logf("GET failed: %v\n", e.Error())
			t.Fail()
		default:
		}
	}

	wg.Wait()
}

func rwstress(t *testing.T) {
	runProviderTests(t, func(t *testing.T, bck cmn.Bck, cksumType string) {
		err := cmn.CreateDir(fmt.Sprintf("%s/%s", baseDir, rwdir))
		tassert.CheckFatal(t, err)
		defer func() {
			err := os.RemoveAll(fmt.Sprintf("%s/%s", baseDir, rwdir))
			tassert.CheckFatal(t, err)
		}()

		var (
			proxyURL = tutils.RandomProxyURL()
		)
		filelock.files = make([]fileLock, numFiles)

		generateRandomData(numFiles)

		var wg sync.WaitGroup
		doneCh := make(chan int, 2)
		wg.Add(1)
		go rwPutLoop(t, proxyURL, bck, fileNames, &wg, cksumType, doneCh)
		wg.Add(1)
		go rwGetLoop(t, proxyURL, bck, fileNames, &wg, doneCh)
		if !skipdel {
			wg.Add(1)
			go rwDelLoop(t, proxyURL, bck, fileNames, &wg, doneCh, rwRunNormal)
		}

		wg.Wait()
		rwDelLoop(t, proxyURL, bck, fileNames, nil, doneCh, rwRunCleanUp)
	})
}

func TestRWStressShort(t *testing.T) {
	numFiles = 25
	numLoops = 8

	rwstress(t)
}

// Test_rwstress runs delete, put, and get operations in a loop
// Since PUT is the longest operation, PUT loop runs the defined number
//    of cycles and emits a done signal at the end. Both GET and DEL run
//    endlessly until PUT loop emits the done signal
// If -nodel is on then the test runs only PUT and GET in a loop and after they
//    complete the test runs DEL loop to clean up
// If the test runs asynchronously all three kinds of operations then after the
//    test finishes it executes extra loop to delete all files
func TestRWStress(t *testing.T) {
	tutils.CheckSkip(t, tutils.SkipTestArgs{Long: true})

	numLoops = cycles
	numFiles = numfiles
	rwstress(t)
}
