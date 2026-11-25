package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"desklink/internal/crypto"
	dlnet "desklink/internal/net"
)

// uploadReplyTimeout bounds waiting for the host's reply once the whole
// file is sent; the transfer itself takes as long as it needs.
const uploadReplyTimeout = time.Minute

// uploadContinueTimeout is how long the body waits for the host's 100
// Continue before it is sent anyway.
const uploadContinueTimeout = 10 * time.Second

// ErrUploadUnsupported means the host is too old to receive files.
var ErrUploadUnsupported = errors.New("the host is too old to receive files; update desklinkd there")

// SendFile sends a file to the host (POST /files), which saves it in its
// download directory, and returns the name the host saved it under. size is
// the length of body, or -1 when unknown; progress, when set, receives the
// bytes sent so far as the body is read. It needs no session: it finds the
// host at any of its addresses and authenticates with a fresh token.
func SendFile(ctx context.Context, p Profile, name string, body io.Reader, size int64, progress func(sent int64)) (string, error) {
	if !p.Paired() {
		return "", errors.New("host is not paired")
	}
	reached, h, err := Reach(ctx, p)
	if err != nil {
		return "", err
	}
	// Stamped with the host's clock, as for Connect.
	token, err := crypto.AuthToken(p.PeerID, p.PeerKey, time.UnixMilli(h.TimeMs))
	if err != nil {
		return "", err
	}
	req, err := uploadRequest(ctx, baseURL(reached.IP, p.Port), name, &countingReader{r: body, progress: progress}, size)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	tlsCfg, _ := pinnedTLS(p.Fingerprint)
	c := &http.Client{Transport: &http.Transport{
		TLSClientConfig: tlsCfg, DisableKeepAlives: true, ResponseHeaderTimeout: uploadReplyTimeout,
		ExpectContinueTimeout: uploadContinueTimeout,
	}}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("send %s: %w", name, err)
	}
	defer closeBody(resp.Body)
	return uploadResult(resp.StatusCode, io.LimitReader(resp.Body, maxReplyBytes))
}

// uploadRequest is the POST /files request for a file name and body.
func uploadRequest(ctx context.Context, base, name string, body io.Reader, size int64) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/files?name="+url.QueryEscape(name), body)
	if err != nil {
		return nil, fmt.Errorf("build upload request: %w", err)
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	// The host refuses a file it will not take (too large, another upload
	// running, a rejected token) before reading it and then closes the
	// connection; a body still being sent would turn that answer into a
	// connection reset. With 100-continue the body waits for the host's go.
	req.Header.Set("Expect", "100-continue")
	return req, nil
}

// uploadResult reads the host's reply to an upload: the saved name, or an
// error with the host's reason.
func uploadResult(status int, body io.Reader) (string, error) {
	var reply dlnet.UploadReply
	decodeErr := json.NewDecoder(body).Decode(&reply)
	switch {
	case status == http.StatusOK && decodeErr == nil && reply.Name != "":
		return reply.Name, nil
	case status == http.StatusUnauthorized:
		return "", errors.New("the host rejected this pairing: pair again")
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		return "", ErrUploadUnsupported
	case reply.Error != "":
		return "", fmt.Errorf("the host refused the file: %s", reply.Error)
	default:
		return "", fmt.Errorf("upload: HTTP %d", status)
	}
}

// countingReader reports how much of r was read.
type countingReader struct {
	r        io.Reader
	n        int64
	progress func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if n > 0 && c.progress != nil {
		c.progress(c.n)
	}
	return n, err
}
