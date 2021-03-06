// Package integration contains AIS integration tests.
/*
 * Copyright (c) 2020, NVIDIA CORPORATION. All rights reserved.
 */
package integration

import (
	"errors"
	"net/http"
	"testing"

	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/tutils"
	"github.com/NVIDIA/aistore/tutils/readers"
	"github.com/NVIDIA/aistore/tutils/tassert"
)

func createBaseParams() (unAuth, auth api.BaseParams) {
	unAuth = tutils.BaseAPIParams()
	auth = tutils.BaseAPIParams()
	auth.Token = tutils.AuthToken
	return
}

func expectUnauthorized(t *testing.T, err error) {
	tassert.Fatalf(t, err != nil, "expected unauthorized error")
	var httpErr *cmn.HTTPError
	tassert.Fatalf(t, errors.As(err, &httpErr), "expected cmn.HTTPError")
	tassert.Fatalf(
		t, httpErr.Status == http.StatusUnauthorized,
		"expected status unauthorized, got: %d", httpErr.Status,
	)
}

func TestAuthObj(t *testing.T) {
	tutils.CheckSkip(t, tutils.SkipTestArgs{RequiresAuth: true})

	var (
		unAuthBP, authBP = createBaseParams()
		bck              = cmn.Bck{
			Name: cmn.RandString(10),
		}
	)

	err := api.CreateBucket(authBP, bck)
	tassert.CheckFatal(t, err)
	defer func() {
		err := api.DestroyBucket(authBP, bck)
		tassert.CheckFatal(t, err)
	}()

	r, _ := readers.NewRandReader(fileSize, cmn.ChecksumNone)
	err = api.PutObject(api.PutObjectArgs{
		BaseParams: unAuthBP,
		Bck:        bck,
		Reader:     r,
		Size:       fileSize,
	})
	expectUnauthorized(t, err)
}

func TestAuthBck(t *testing.T) {
	tutils.CheckSkip(t, tutils.SkipTestArgs{RequiresAuth: true})

	var (
		unAuthBP, authBP = createBaseParams()
		bck              = cmn.Bck{
			Name: cmn.RandString(10),
		}
	)

	err := api.CreateBucket(unAuthBP, bck)
	expectUnauthorized(t, err)

	err = api.CreateBucket(authBP, bck)
	tassert.CheckFatal(t, err)
	defer func() {
		api.DestroyBucket(authBP, bck)
	}()

	err = api.DestroyBucket(unAuthBP, bck)
	expectUnauthorized(t, err)
}
