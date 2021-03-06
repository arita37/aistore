// Package cmn provides common low-level types and utilities for all aistore projects
/*
 * Copyright (c) 2018, NVIDIA CORPORATION. All rights reserved.
 */
package cmn

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/NVIDIA/aistore/3rdparty/glog"
	jsoniter "github.com/json-iterator/go"
)

var (
	// It is used to Marshal/Unmarshal API json messages and is initialized in init function.
	jsonAPI jsoniter.API
)

type (
	// Error structure for HTTP errors
	HTTPError struct {
		Status     int    `json:"status"`
		Message    string `json:"message"`
		Method     string `json:"method"`
		URLPath    string `json:"url_path"`
		RemoteAddr string `json:"remote_addr"`
		Trace      string `json:"trace"`
	}

	// ReqArgs specifies http request that we want to send
	ReqArgs struct {
		Method string      // GET, POST, ...
		Header http.Header // request headers
		Base   string      // base URL: http://xyz.abc
		Path   string      // path URL: /x/y/z
		Query  url.Values  // query: ?x=y&y=z
		Body   []byte      // body for POST, PUT, ...
		BodyR  io.Reader
	}
)

// Eg: Bad Request: Bucket abc does not appear to be local or does not exist:
//   DELETE /v1/buckets/abc from 127.0.0.1:54064| ([httpcommon.go, #840] <- [proxy.go, #484] <- [proxy.go, #264])
func (e *HTTPError) String() string {
	return http.StatusText(e.Status) + ": " + e.Message + ": " + e.Method + " " + e.URLPath + " from " + e.RemoteAddr + "| (" + e.Trace + ")"
}

// Implements error interface
func (e *HTTPError) Error() string {
	// Stop from escaping <, > ,and &
	buf := new(bytes.Buffer)
	enc := jsoniter.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return err.Error()
	}
	return buf.String()
}

// NewHTTPError returns a HTTPError struct. There are cases
// where the message is already formatted as a HTTPError (from target)
// in which case returns `true`, otherwise `false`.
//
// NOTE: The format of the error message is being used in the CLI.
//  If there are any changes, please make sure to update `errorHandler`
//  in the CLI.
func NewHTTPError(r *http.Request, msg string, status int) (*HTTPError, bool) {
	var httpErr HTTPError
	if err := jsoniter.UnmarshalFromString(msg, &httpErr); err == nil {
		return &httpErr, true
	}
	return &HTTPError{Status: status, Message: msg, Method: r.Method, URLPath: r.URL.Path, RemoteAddr: r.RemoteAddr}, false
}

// URLPath returns a HTTP URL path by joining all segments with "/"
func URLPath(segments ...string) string {
	return path.Join("/", path.Join(segments...))
}

// PrependProtocol prepends protocol in URL in case it is missing.
// By default it adds `http://` as prefix to the URL.
func PrependProtocol(url string, protocol ...string) string {
	if url == "" || strings.Contains(url, "://") {
		return url
	}
	proto := httpProto
	if len(protocol) == 1 {
		proto = protocol[0]
	}
	return proto + "://" + url
}

// RESTItems splits whole path into the items.
func RESTItems(unescapedPath string) []string {
	escaped := html.EscapeString(unescapedPath)
	split := strings.Split(escaped, "/")
	apiItems := split[:0] // filtering without allocation
	for _, item := range split {
		if item != "" { // omit empty
			apiItems = append(apiItems, item)
		}
	}
	return apiItems
}

// MatchRESTItems splits url path into api items and match them with provided
// items. If splitAfter is set to true all items will be split, otherwise the
// rest of the path will be splited only to itemsAfter items. Returns all items
// which come after all of the provided items
func MatchRESTItems(unescapedPath string, itemsAfter int, splitAfter bool, items ...string) ([]string, error) {
	var split []string
	escaped := html.EscapeString(unescapedPath)
	if len(escaped) > 0 && escaped[0] == '/' {
		escaped = escaped[1:] // remove leading slash
	}
	if splitAfter {
		split = strings.Split(escaped, "/")
	} else {
		split = strings.SplitN(escaped, "/", len(items)+Max(1, itemsAfter))
	}
	apiItems := split[:0] // filtering without allocation
	for _, item := range split {
		if item != "" { // omit empty
			apiItems = append(apiItems, item)
		}
	}

	if len(apiItems) < len(items) {
		return nil, fmt.Errorf("expected %d items, but got: %d", len(items), len(apiItems))
	}

	for idx, item := range items {
		if item != apiItems[idx] {
			return nil, fmt.Errorf("expected %s in path, but got: %s", item, apiItems[idx])
		}
	}

	apiItems = apiItems[len(items):]
	if len(apiItems) < itemsAfter {
		return nil, fmt.Errorf("path is too short: got %d items, but expected %d", len(apiItems)+len(items), itemsAfter+len(items))
	}

	return apiItems, nil
}

func InvalidHandler(w http.ResponseWriter, r *http.Request) {
	InvalidHandlerWithMsg(w, r, "invalid request")
}

// InvalidHandlerWithMsg writes error to response writer.
func InvalidHandlerWithMsg(w http.ResponseWriter, r *http.Request, msg string, errCode ...int) {
	status := http.StatusBadRequest
	if len(errCode) != 0 {
		status = errCode[0]
	}

	err, _ := NewHTTPError(r, msg, status)
	writeError(w, err, status)
}

func invalidHandlerInternal(w http.ResponseWriter, r *http.Request, msg string, status int, silent bool) {
	err, isHTTPError := NewHTTPError(r, msg, status)

	if silent {
		writeError(w, err, status)
		return
	}
	if isHTTPError {
		glog.Errorln(err.String())
		writeError(w, err, status)
		return
	}
	var errMsg bytes.Buffer
	if !strings.Contains(msg, ".go, #") {
		for i := 1; i < 5; i++ {
			if _, file, line, ok := runtime.Caller(i); ok {
				f := filepath.Base(file)
				if i > 1 {
					errMsg.WriteString(" <- ")
				}
				fmt.Fprintf(&errMsg, "[%s, #%d]", f, line)
			}
		}
	}
	err.Trace = errMsg.String()
	glog.Errorln(err.String())
	writeError(w, err, status)
}

// writeError is slightly updated `http.Error` to change `Content-Type` header.
// Content type was adjusted to make sure that the caller is aware that we return
// JSON error and not just a regular string message.
func writeError(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	fmt.Fprintln(w, err.Error())
}

// InvalidHandlerDetailed writes detailed error (includes line and file) to response writer.
func InvalidHandlerDetailed(w http.ResponseWriter, r *http.Request, msg string, errCode ...int) {
	status := http.StatusBadRequest
	if len(errCode) > 0 && errCode[0] >= http.StatusBadRequest {
		status = errCode[0]
	}

	invalidHandlerInternal(w, r, msg, status, false /*silent*/)
}

// InvalidHandlerDetailedNoLog writes detailed error (includes line and file) to response writer. It does not log any error
func InvalidHandlerDetailedNoLog(w http.ResponseWriter, r *http.Request, msg string, errCode ...int) {
	status := http.StatusBadRequest
	if len(errCode) > 0 && errCode[0] >= http.StatusBadRequest {
		status = errCode[0]
	}

	invalidHandlerInternal(w, r, msg, status, true /*silent*/)
}

func ReadBytes(r *http.Request) (b []byte, err error) {
	var (
		e error
	)

	b, e = ioutil.ReadAll(r.Body)
	if e != nil {
		err = fmt.Errorf("failed to read %s request, err: %v", r.Method, e)
		if e == io.EOF {
			trailer := r.Trailer.Get("Error")
			if trailer != "" {
				err = fmt.Errorf("failed to read %s request, err: %v, trailer: %s", r.Method, e, trailer)
			}
		}
	}
	r.Body.Close()

	return b, err
}

func ReadJSON(w http.ResponseWriter, r *http.Request, out interface{}, optional ...bool) error {
	defer r.Body.Close()
	if err := jsoniter.NewDecoder(r.Body).Decode(out); err != nil {
		if len(optional) > 0 && optional[0] && err == io.EOF {
			return nil
		}
		s := fmt.Sprintf("failed to json-unmarshal %s request, err: %v [%T]", r.Method, err, out)
		if _, file, line, ok := runtime.Caller(1); ok {
			f := filepath.Base(file)
			s += fmt.Sprintf("(%s, #%d)", f, line)
		}
		InvalidHandlerDetailed(w, r, s)
		return err
	}
	return nil
}

// MustMarshal marshals v and panics if error occurs.
func MustMarshal(v interface{}) []byte {
	b, err := jsonAPI.Marshal(v)
	AssertNoErr(err)
	return b
}

func TryUnmarshal(data, v interface{}) error {
	x := reflect.ValueOf(v)
	Assert(x.Kind() == reflect.Ptr)

	// `data` can be of type `map[string]interface{}` or just same type as `v`.
	// Therefore, the easiest way is to marshal the `data` again and unmarshal it
	// with hope that every field will be set correctly.
	b := MustMarshal(data)
	return jsonAPI.Unmarshal(b, v)
}

func (u *ReqArgs) URL() string {
	url := strings.TrimSuffix(u.Base, "/")
	if !strings.HasPrefix(u.Path, "/") {
		url += "/"
	}
	url += u.Path
	query := u.Query.Encode()
	if query != "" {
		url += "?" + query
	}
	return url
}

func (u *ReqArgs) Req() (*http.Request, error) {
	r := u.BodyR
	if r == nil && u.Body != nil {
		r = bytes.NewBuffer(u.Body)
	}

	req, err := http.NewRequest(u.Method, u.URL(), r)
	if err != nil {
		return nil, err
	}

	if u.Header != nil {
		copyHeaders(u.Header, &req.Header)
	}

	return req, nil
}

// ReqWithCancel creates request with ability to cancel it.
func (u *ReqArgs) ReqWithCancel() (*http.Request, context.Context, context.CancelFunc, error) {
	req, err := u.Req()
	if err != nil {
		return nil, nil, nil, err
	}
	if u.Method == http.MethodPost || u.Method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
	}
	ctx, cancel := context.WithCancel(context.Background())
	req = req.WithContext(ctx)
	return req, ctx, cancel, nil
}

func (u *ReqArgs) ReqWithTimeout(timeout time.Duration) (*http.Request, context.Context, context.CancelFunc, error) {
	req, err := u.Req()
	if err != nil {
		return nil, nil, nil, err
	}
	if u.Method == http.MethodPost || u.Method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	req = req.WithContext(ctx)
	return req, ctx, cancel, nil
}

// Copies headers from original request(from client) to
// a new one(inter-cluster call)
func copyHeaders(src http.Header, dst *http.Header) {
	for k, values := range src {
		for _, v := range values {
			dst.Set(k, v)
		}
	}
}

func MakeHeaderAuthnToken(token string) string {
	return HeaderBearer + " " + token
}

func IsHTTPS(urlPath string) bool {
	return strings.HasPrefix(urlPath, "https://")
}
