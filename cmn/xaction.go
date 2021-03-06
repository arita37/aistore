// Package cmn provides common API constants and types, and low-level utilities for all aistore projects
/*
 * Copyright (c) 2018, NVIDIA CORPORATION. All rights reserved.
 */
package cmn

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NVIDIA/aistore/3rdparty/atomic"
	"github.com/NVIDIA/aistore/3rdparty/glog"
)

const timeStampFormat = "15:04:05.000000"

const xactIdleTimeout = time.Minute * 3

type (
	XactID interface {
		String() string
		Int() int64
		Compare(string) int // -1 = less, 0 = equal, +1 = greater
	}

	Xact interface {
		XactCountStats
		ID() XactID
		Kind() string
		Bck() Bck
		SetBucket(bucket string)
		StartTime(s ...time.Time) time.Time
		EndTime(e ...time.Time) time.Time
		String() string
		Abort()
		ChanAbort() <-chan struct{}
		Finished() bool
		Aborted() bool
		IsMountpathXact() bool
		Result() (interface{}, error)
	}
	XactBase struct {
		XactBaseCountStats
		id      XactID
		sutime  atomic.Int64
		eutime  atomic.Int64
		kind    string
		bck     Bck
		abrt    chan struct{}
		aborted atomic.Bool
	}
	XactBaseID string

	//
	// xaction that self-terminates after staying idle for a while
	// with an added capability to renew itself and ref-count its pending work
	//
	XactDemand interface {
		Xact
		ChanCheckTimeout() <-chan time.Time
		Renew()
		Timeout() bool
		IncPending()
		DecPending()
	}
	XactDemandBase struct {
		XactBase
		ticker  *time.Ticker
		renew   atomic.Int64
		pending atomic.Int64
	}
	ErrXactExpired struct { // return it if called (right) after self-termination
		msg string
	}

	MountpathXact    struct{}
	NonmountpathXact struct{}

	XactRunningStatus struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		Bck     Bck    `json:"bck"`
		Running bool   `json:"running"`
	}
)

func (e *ErrXactExpired) Error() string            { return e.msg }
func NewErrXactExpired(msg string) *ErrXactExpired { return &ErrXactExpired{msg: msg} }
func IsErrXactExpired(err error) bool              { _, ok := err.(*ErrXactExpired); return ok }

func (id XactBaseID) String() string           { return string(id) }
func (id XactBaseID) Int() int64               { Assert(false); return 0 }
func (id XactBaseID) Compare(other string) int { return strings.Compare(string(id), other) }

//
// XactBase - partially implements Xact interface
//

func NewXactBase(id XactID, kind string) *XactBase {
	stime := time.Now()
	Assert(kind != "")
	xact := &XactBase{id: id, kind: kind, abrt: make(chan struct{})}
	xact.sutime.Store(stime.UnixNano())
	return xact
}
func NewXactBaseWithBucket(id, kind string, bck Bck) *XactBase {
	xact := NewXactBase(XactBaseID(id), kind)
	xact.bck = bck
	return xact
}

func (xact *XactBase) ID() XactID                 { return xact.id }
func (xact *XactBase) Kind() string               { return xact.kind }
func (xact *XactBase) Bck() Bck                   { return xact.bck }
func (xact *XactBase) Finished() bool             { return xact.eutime.Load() != 0 }
func (xact *XactBase) ChanAbort() <-chan struct{} { return xact.abrt }
func (xact *XactBase) Aborted() bool              { return xact.aborted.Load() }

func (xact *XactBase) SetBucket(bucket string) { xact.bck.Name = bucket }

func (xact *XactBase) String() string {
	var (
		prefix = xact.Kind()
	)
	if xact.bck.Name != "" {
		prefix += "@" + xact.bck.Name
	}
	if !xact.Finished() {
		return fmt.Sprintf("%s(%s)", prefix, xact.ID())
	}
	var (
		stime    = xact.StartTime()
		stimestr = stime.Format(timeStampFormat)
		etime    = xact.EndTime()
		d        = etime.Sub(stime)
	)
	return fmt.Sprintf("%s(%s) started %s ended %s (%v)",
		prefix, xact.ID(), stimestr, etime.Format(timeStampFormat), d)
}

func (xact *XactBase) StartTime(s ...time.Time) time.Time {
	if len(s) == 0 {
		u := xact.sutime.Load()
		if u == 0 {
			return time.Time{}
		}
		return time.Unix(0, u)
	}
	stime := s[0]
	xact.sutime.Store(stime.UnixNano())
	return stime
}

func (xact *XactBase) EndTime(e ...time.Time) time.Time {
	if len(e) == 0 {
		u := xact.eutime.Load()
		if u == 0 {
			return time.Time{}
		}
		return time.Unix(0, u)
	}
	etime := e[0]
	xact.eutime.Store(etime.UnixNano())
	if xact.Kind() != ActAsyncTask {
		glog.Infoln(xact.String())
	}
	return etime
}

func (xact *XactBase) Abort() {
	if !xact.aborted.CAS(false, true) {
		glog.Infof("already aborted: " + xact.String())
		return
	}
	xact.eutime.Store(time.Now().UnixNano())
	close(xact.abrt)
	glog.Infof("ABORT: " + xact.String())
}

func (xact *XactBase) Result() (interface{}, error) {
	return nil, errors.New("getting result is not implemented")
}

//
// XactDemandBase - partially implements XactDemand interface
//

func NewXactDemandBase(kind string, bck Bck, idleTime ...time.Duration) *XactDemandBase {
	tickTime := xactIdleTimeout
	if len(idleTime) != 0 {
		tickTime = idleTime[0]
	}
	ticker := time.NewTicker(tickTime)
	return &XactDemandBase{
		XactBase: *NewXactBaseWithBucket("", kind, bck),
		ticker:   ticker,
	}
}

func (r *XactDemandBase) ChanCheckTimeout() <-chan time.Time { return r.ticker.C }
func (r *XactDemandBase) Renew()                             { r.renew.Store(1) } // see Timeout()
func (r *XactDemandBase) IncPending()                        { r.pending.Inc() }
func (r *XactDemandBase) DecPending()                        { r.pending.Dec() }
func (r *XactDemandBase) SubPending(n int64)                 { r.pending.Sub(n) }
func (r *XactDemandBase) Pending() int64                     { return r.pending.Load() }

func (r *XactDemandBase) Timeout() bool {
	if r.pending.Load() > 0 {
		return false
	}
	return r.renew.Dec() < 0
}

func (r *XactDemandBase) Stop() { r.ticker.Stop() }

func IsValidXaction(kind string) bool {
	_, ok := XactsMeta[kind]
	return ok
}

func IsXactTypeBck(kind string) bool {
	return XactsMeta[kind].Type == XactTypeBck
}

func (*MountpathXact) IsMountpathXact() bool    { return true }
func (*NonmountpathXact) IsMountpathXact() bool { return false }

type (
	XactCountStats interface {
		ObjectsCnt() int64
		BytesCnt() int64
	}

	XactBaseCountStats struct {
		objects atomic.Int64
		bytes   atomic.Int64
	}
)

func (s *XactBaseCountStats) ObjectsCnt() int64          { return s.objects.Load() }
func (s *XactBaseCountStats) ObjectsInc() int64          { return s.objects.Inc() }
func (s *XactBaseCountStats) ObjectsAdd(cnt int64) int64 { return s.objects.Add(cnt) }

func (s *XactBaseCountStats) BytesCnt() int64           { return s.bytes.Load() }
func (s *XactBaseCountStats) BytesAdd(size int64) int64 { return s.bytes.Add(size) }
