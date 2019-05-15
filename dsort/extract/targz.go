// Package extract provides provides functions for working with compressed files
/*
 * Copyright (c) 2018, NVIDIA CORPORATION. All rights reserved.
 */
package extract

import (
	"archive/tar"
	"compress/gzip"
	"io"

	"github.com/NVIDIA/aistore/3rdparty/glog"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/memsys"
	jsoniter "github.com/json-iterator/go"
)

var (
	_ ExtractCreator = &targzExtractCreator{}
)

// tarFileHeader represents a single record's file metadata. The fields here are
// taken from tar.Header. It is very costly to marshal and unmarshal time.Time
// to and from JSON, so all time.Time fields are omitted. Furthermore, the
// time.Time fields are updated upon creating the new tarballs, so there is no
// need to maintain the original values.
type tarFileHeader struct {
	Typeflag byte `json:"typeflag"` // Type of header entry (should be TypeReg for most files)

	Name     string `json:"name"`     // Name of file entry
	Linkname string `json:"linkname"` // Target name of link (valid for TypeLink or TypeSymlink)

	Size  int64  `json:"size"`  // Logical file size in bytes
	Mode  int64  `json:"mode"`  // Permission and mode bits
	UID   int    `json:"uid"`   // User ID of owner
	GID   int    `json:"gid"`   // Group ID of owner
	Uname string `json:"uname"` // User name of owner
	Gname string `json:"gname"` // Group name of owner
}

type targzExtractCreator struct{}

// targzRecordDataReader is used for writing metadata as well as data to the buffer.
type targzRecordDataReader struct {
	slab *memsys.Slab2

	metadataSize int64
	size         int64
	written      int64
	metadataBuf  []byte
	tarWriter    *tar.Writer
}

func newTargzRecordDataReader() *targzRecordDataReader {
	rd := &targzRecordDataReader{}
	rd.metadataBuf, rd.slab = mem.AllocFromSlab2(cmn.KiB)
	return rd
}

func (rd *targzRecordDataReader) reinit(tw *tar.Writer, size int64, metadataSize int64) {
	rd.tarWriter = tw
	rd.written = 0
	rd.size = size
	rd.metadataSize = metadataSize
}

func (rd *targzRecordDataReader) free() {
	rd.slab.Free(rd.metadataBuf)
}

func (rd *targzRecordDataReader) Write(p []byte) (int, error) {
	// Write header
	remainingMetadataSize := rd.metadataSize - rd.written
	if remainingMetadataSize > 0 {
		if int64(len(p)) < remainingMetadataSize {
			copy(rd.metadataBuf[rd.written:], p)
			rd.written += int64(len(p))
			return len(p), nil
		}

		copy(rd.metadataBuf[rd.written:], p[:remainingMetadataSize])
		rd.written += remainingMetadataSize
		p = p[remainingMetadataSize:]
		var metadata tarFileHeader
		if err := jsoniter.Unmarshal(rd.metadataBuf[:rd.metadataSize], &metadata); err != nil {
			return int(remainingMetadataSize), err
		}

		header := &tar.Header{
			Size:     rd.size,
			Name:     metadata.Name,
			Typeflag: metadata.Typeflag,
			Linkname: metadata.Linkname,
			Mode:     metadata.Mode,
			Uid:      metadata.UID,
			Gid:      metadata.GID,
			Uname:    metadata.Uname,
			Gname:    metadata.Gname,
		}

		if err := rd.tarWriter.WriteHeader(header); err != nil {
			return int(remainingMetadataSize), err
		}
	} else {
		remainingMetadataSize = 0
	}

	n, err := rd.tarWriter.Write(p)
	rd.written += int64(n)
	return n + int(remainingMetadataSize), err
}

// ExtractShard reads the tarball f and extracts its metadata.
func (t *targzExtractCreator) ExtractShard(fqn string, r *io.SectionReader, extractor RecordExtractor, toDisk bool) (extractedSize int64, extractedCount int, err error) {
	var (
		size   int64
		header *tar.Header
	)

	gzr, err := gzip.NewReader(r)
	if err != nil {
		return 0, 0, err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)

	var slabSize int64 = memsys.MaxSlabSize
	if r.Size() < cmn.MiB {
		slabSize = 128 * cmn.KiB
	}

	slab, err := mem.GetSlab2(slabSize)
	cmn.AssertNoErr(err)
	buf := slab.Alloc()
	defer slab.Free(buf)

	for {
		header, err = tr.Next()
		if err == io.EOF {
			return extractedSize, extractedCount, nil
		} else if err != nil {
			return extractedSize, extractedCount, err
		}

		metadata := tarFileHeader{
			Name:     header.Name,
			Typeflag: header.Typeflag,
			Linkname: header.Linkname,
			Mode:     header.Mode,
			UID:      header.Uid,
			GID:      header.Gid,
			Uname:    header.Uname,
			Gname:    header.Gname,
		}

		bmeta, err := jsoniter.Marshal(metadata)
		if err != nil {
			return extractedSize, extractedCount, err
		}

		if header.Typeflag == tar.TypeDir {
			// We can safely ignore this case because we do `MkdirAll` anyway
			// when we create files. And since dirs can appear after all the files
			// we must have this `MkdirAll` before files.
			continue
		} else if header.Typeflag == tar.TypeReg {
			data := cmn.NewSizedReader(tr, header.Size)
			if size, err = extractor.ExtractRecordWithBuffer(t, fqn, header.Name, data, bmeta, toDisk, 0, buf); err != nil {
				return extractedSize, extractedCount, err
			}
		} else {
			glog.Warningf("Unrecognized header typeflag in tar: %s", string(header.Typeflag))
			continue
		}

		extractedSize += size
		extractedCount++
	}
}

func NewTargzExtractCreator() ExtractCreator {
	return &targzExtractCreator{}
}

// CreateShard creates a new shard locally based on the Shard.
// Note that the order of closing must be trw, gzw, then finally tarball.
func (t *targzExtractCreator) CreateShard(s *Shard, tarball io.Writer, loadContent LoadContentFunc) (written int64, err error) {
	var (
		n int64
	)

	gzw := gzip.NewWriter(tarball)
	tw := tar.NewWriter(gzw)
	defer func() {
		tw.Close()
		gzw.Close()
	}()

	rdReader := newTargzRecordDataReader()
	for _, rec := range s.Records.All() {
		for _, obj := range rec.Objects {
			rdReader.reinit(tw, obj.Size, obj.MetadataSize)
			if n, err = loadContent(rdReader, rec, obj); err != nil {
				return written + n, err
			}

			written += n
		}
	}
	rdReader.free()
	return written, nil
}

func (t *targzExtractCreator) UsingCompression() bool {
	return true
}

func (t *targzExtractCreator) SupportsOffset() bool {
	return false
}

func (t *targzExtractCreator) MetadataSize() int64 {
	return tarBlockSize // size of tar header with padding
}