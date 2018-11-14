/*
 * Copyright (c) 2018, NVIDIA CORPORATION. All rights reserved.
 *
 */
// Package cluster provides common interfaces and local access to cluster-level metadata
package cluster

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/dfcpub/3rdparty/glog"
	"github.com/NVIDIA/dfcpub/cmn"
	"github.com/NVIDIA/dfcpub/fs"
)

/*
 * Besides objects we must to deal with additional files like: workfiles, dsort
 * intermediate files (used when spilling to disk) or EC slices. These files can
 * have different rules of rebalancing, evicting and other processing. Each
 * content type needs to implement ContentResolver to reflect the rules and
 * permission for different services. To see how the interface can be
 * implemented see: DefaultWorkfile implemention.
 *
 * When walking through the files we need to know if the file is an object or
 * other content. To do that we generate fqn with GenContentFQN. It adds short
 * prefix to the base name, which we believe is unique and will separate objects
 * from content files. When parsing we check if the fqn prefix match
 * workfilePrefix (to quickly separate objects from non-objects) and we parse
 * the file type to run ParseUniqueFQN (implemented by this file type) on the rest of the base name.
 */

const (
	workfilePrefix      = ".~~~."
	DefaultWorkfileType = "" // empty for backward compatibility
)

type (
	ContentResolver interface {
		// When set to true, services like rebalance have permission to move
		// content for example to another target because it is misplaced (HRW).
		PermToMove() bool
		// When set to true, services like LRU have permission to evict/delete content.
		PermToEvict() bool
		// When set to true, content can be checksumed, shown or processed in other ways.
		PermToProcess() bool

		// Generates unique base name for original one. This function may add
		// additional information to the base name.
		GenUniqueFQN(base string) (ufqn string)
		// Parses generated unique fqn to the original one.
		ParseUniqueFQN(base string) (orig string, old bool, ok bool)
	}

	ContentInfo struct {
		Dir  string // Original directory
		Base string // Original base name of the file
		Old  bool   // Determines if the file is old or not
		Type string // Type of the workfile
	}

	// DefaultWorkfile implements the ContentResolver interface.
	DefaultWorkfile struct{}
)

var (
	registeredFileTypes map[string]ContentResolver
	pid                 int64 = 0xDEADBEEF   // pid of the current process
	spid                      = "0xDEADBEEF" // string version of the pid
)

func init() {
	pid = int64(os.Getpid())
	spid = strconv.FormatInt(pid, 16)
	registeredFileTypes = make(map[string]ContentResolver, 10)

	defaultSpec := &DefaultWorkfile{}
	RegisterFileType(DefaultWorkfileType, defaultSpec)
}

// RegisterFileType registers new workfile type with given content resolver.
//
// NOTE: fileType should not contain dot since it is separator for additional
// info and parsing can fail.
//
// NOTE: FIXME: All registration must happen at the startup, otherwise panic can
// be expected.
func RegisterFileType(fileType string, spec ContentResolver) error {
	if strings.Index(fileType, ".") != -1 {
		return fmt.Errorf("file type %s should not contain dot '.'", fileType)
	}

	if _, ok := registeredFileTypes[fileType]; ok {
		return fmt.Errorf("file type %s is already registered", fileType)
	}
	registeredFileTypes[fileType] = spec
	return nil
}

// GenContentFQN returns new fqn generated from given fqn. Generated fqn will
// contain additional info which will then speed up the parsing process -
// parsing is on the data path so we care about performance very much. Things
// that are added are: workfilePrefix and fileType.
func GenContentFQN(fqn string, fileType string) string {
	dir, base := filepath.Split(fqn)
	cmn.Assert(strings.HasSuffix(dir, "/"), fqn)
	cmn.Assert(base != "", fqn)

	spec, ok := registeredFileTypes[fileType]
	if !ok {
		cmn.Assert(false, fmt.Sprintf("file type %s was not registered", fileType))
	}

	return dir + workfilePrefix + fileType + "." + spec.GenUniqueFQN(base)
}

// FileSpec returns the specification/attributes and information about fqn. spec
// and info are only set when fqn was generated by GenContentFQN.
func FileSpec(fqn string) (resolver ContentResolver, info *ContentInfo) {
	dir, base := filepath.Split(fqn)
	if !strings.HasSuffix(dir, "/") {
		return nil, nil
	}
	if !strings.HasPrefix(base, workfilePrefix) {
		return nil, nil
	}
	if base == "" {
		return nil, nil
	}
	// cold path - it is workfile
	prefixIndex := len(workfilePrefix)
	fileTypeIndex := strings.Index(base[prefixIndex:], ".")
	if fileTypeIndex < 0 {
		return nil, nil
	}
	fileTypeIndex += prefixIndex
	spec, found := registeredFileTypes[base[prefixIndex:fileTypeIndex]]
	if !found {
		// Quite weird, seemed like workfile but in the end it isn't
		glog.Warningf("fqn: %q looked like a workfile but does not match any registered workfile type", fqn)
		return nil, nil
	}
	origBase, old, ok := spec.ParseUniqueFQN(base[fileTypeIndex+1:])
	if !ok {
		return nil, nil
	}

	return spec, &ContentInfo{
		Dir:  dir,
		Base: origBase,
		Old:  old,
		Type: base[prefixIndex:fileTypeIndex],
	}
}

func (wf *DefaultWorkfile) PermToMove() bool    { return false }
func (wf *DefaultWorkfile) PermToEvict() bool   { return false }
func (wf *DefaultWorkfile) PermToProcess() bool { return false }

func (wf *DefaultWorkfile) GenUniqueFQN(base string) string {
	tieBreaker := strconv.FormatInt(time.Now().UnixNano(), 16)
	return base + "." + tieBreaker[5:] + "." + spid
}

func (wf *DefaultWorkfile) ParseUniqueFQN(base string) (orig string, old bool, ok bool) {
	pidIndex := strings.LastIndex(base, ".") // pid
	if pidIndex < 0 {
		return "", false, false
	}
	tieIndex := strings.LastIndex(base[:pidIndex], ".") // tie breaker
	if tieIndex < 0 {
		return "", false, false
	}
	filePID, err := strconv.ParseInt(base[pidIndex+1:], 16, 64)
	if err != nil {
		return "", false, false
	}

	return base[:tieIndex], filePID != pid, true
}

//
// (bucket, object) => FQN => (bucket, object)
//

// (bucket, object) => (local hashed path, fully qualified name aka fqn & error)
func FQN(bucket, objname string, islocal bool) (string, string) {
	mpath, errstr := hrwMpath(bucket, objname)
	if errstr != "" {
		return "", errstr
	}
	if islocal {
		return filepath.Join(fs.Mountpaths.MakePathLocal(mpath), bucket, objname), ""
	}
	return filepath.Join(fs.Mountpaths.MakePathCloud(mpath), bucket, objname), ""
}

// fqn => (bucket, objname, err)
func ResolveFQN(fqn string, bowner Bowner) (bucket, objname string, err error) {
	var (
		islocal   bool
		parsedFQN fs.FQNparsed
	)
	parsedFQN, err = fs.Mountpaths.FQN2Info(fqn)
	if err != nil {
		return
	}
	bucket, objname, islocal = parsedFQN.Bucket, parsedFQN.Objname, parsedFQN.IsLocal
	resfqn, errstr := FQN(bucket, objname, islocal)
	if errstr != "" {
		err = errors.New(errstr)
		return
	}
	errstr = fmt.Sprintf("Cannot convert %s => %s/%s", fqn, bucket, objname)
	if resfqn != fqn {
		err = fmt.Errorf("%s - %q misplaced", errstr, resfqn)
		return
	}
	bmd := bowner.Get()
	if bmd.IsLocal(bucket) != islocal {
		err = fmt.Errorf("%s - islocal mismatch(%t, %t)", errstr, bmd.IsLocal(bucket), islocal)
	}
	return
}
