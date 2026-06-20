package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/pawlenartowicz/leyline/protocol/vaultaddr"
)

// serverBaseAndVault parses a canonical "host/vaultID" address into the HTTPS
// base URL and the vault ID component for constructing /_leyline/api/v1/{vault}/…
// request paths.
func serverBaseAndVault(addr string) (string, string, error) {
	host, vault, err := vaultaddr.Parse(addr)
	if err != nil {
		return "", "", err
	}
	return "https://" + host, vault, nil
}

// doRequest executes an authenticated HTTPS request to /_leyline/api/v1/{vault}/{path}.
// Non-2xx responses are returned as *UpstreamError (body is consumed); network
// or parse errors are returned as the third value.
func (d *Daemon) doRequest(method, path string, body io.Reader) (*http.Response, *UpstreamError, error) {
	base, vault, err := serverBaseAndVault(d.cfg.Vault)
	if err != nil {
		return nil, nil, err
	}
	full := fmt.Sprintf("%s/_leyline/api/v1/%s/%s", base, vault, path)
	req, err := http.NewRequest(method, full, body)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode/100 != 2 {
		buf, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &UpstreamError{Status: resp.StatusCode, Body: buf}, nil
	}
	return resp, nil, nil
}

// Tag proxies POST /_leyline/api/v1/{vault}/tag.
func (d *Daemon) Tag(name, commit string) (RefResponse, *UpstreamError) {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"name": name, "commit": commit})
	resp, upErr, err := d.doRequest("POST", "tag", &buf)
	if err != nil {
		return RefResponse{}, &UpstreamError{Status: 0, Body: []byte(err.Error())}
	}
	if upErr != nil {
		return RefResponse{}, upErr
	}
	defer resp.Body.Close()
	var out RefResponse
	json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

// Review proxies POST /_leyline/api/v1/{vault}/review.
func (d *Daemon) Review(commit string) (RefResponse, *UpstreamError) {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"commit": commit})
	resp, upErr, err := d.doRequest("POST", "review", &buf)
	if err != nil {
		return RefResponse{}, &UpstreamError{Status: 0, Body: []byte(err.Error())}
	}
	if upErr != nil {
		return RefResponse{}, upErr
	}
	defer resp.Body.Close()
	var out RefResponse
	json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

// Revert proxies POST /_leyline/api/v1/{vault}/revert.
func (d *Daemon) Revert(commit string) (RevertResponse, *UpstreamError) {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"commit": commit})
	resp, upErr, err := d.doRequest("POST", "revert", &buf)
	if err != nil {
		return RevertResponse{}, &UpstreamError{Status: 0, Body: []byte(err.Error())}
	}
	if upErr != nil {
		return RevertResponse{}, upErr
	}
	defer resp.Body.Close()
	var out RevertResponse
	json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

// Restore proxies POST /_leyline/api/v1/{vault}/restore.
func (d *Daemon) Restore(commit string) (RefResponse, *UpstreamError) {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"commit": commit})
	resp, upErr, err := d.doRequest("POST", "restore", &buf)
	if err != nil {
		return RefResponse{}, &UpstreamError{Status: 0, Body: []byte(err.Error())}
	}
	if upErr != nil {
		return RefResponse{}, upErr
	}
	defer resp.Body.Close()
	var out RefResponse
	json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

// DeleteTag proxies DELETE /_leyline/api/v1/{vault}/tag/{name}.
func (d *Daemon) DeleteTag(name string) (TagDeleteResponse, *UpstreamError) {
	resp, upErr, err := d.doRequest("DELETE", "tag/"+url.PathEscape(name), nil)
	if err != nil {
		return TagDeleteResponse{}, &UpstreamError{Status: 0, Body: []byte(err.Error())}
	}
	if upErr != nil {
		return TagDeleteResponse{}, upErr
	}
	defer resp.Body.Close()
	var out TagDeleteResponse
	json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

// DeleteTagsByCommit proxies DELETE /_leyline/api/v1/{vault}/tags?commit=<sha>.
func (d *Daemon) DeleteTagsByCommit(commit string) (TagDeleteResponse, *UpstreamError) {
	resp, upErr, err := d.doRequest("DELETE", "tags?commit="+url.QueryEscape(commit), nil)
	if err != nil {
		return TagDeleteResponse{}, &UpstreamError{Status: 0, Body: []byte(err.Error())}
	}
	if upErr != nil {
		return TagDeleteResponse{}, upErr
	}
	defer resp.Body.Close()
	var out TagDeleteResponse
	json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

// Log proxies GET /_leyline/api/v1/{vault}/log.
func (d *Daemon) Log(q LogQuery) (LogResponse, *UpstreamError) {
	base, vault, err := serverBaseAndVault(d.cfg.Vault)
	if err != nil {
		return nil, &UpstreamError{Status: 0, Body: []byte(err.Error())}
	}
	u, _ := url.Parse(fmt.Sprintf("%s/_leyline/api/v1/%s/log", base, vault))
	qv := u.Query()
	if q.Limit > 0 {
		qv.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Before != "" {
		qv.Set("before", q.Before)
	}
	if q.Since != "" {
		qv.Set("since", q.Since)
	}
	if q.Ref != "" {
		qv.Set("ref", q.Ref)
	}
	u.RawQuery = qv.Encode()
	req, _ := http.NewRequest("GET", u.String(), nil)
	req.Header.Set("Authorization", "Bearer "+d.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, &UpstreamError{Status: 0, Body: []byte(err.Error())}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		buf, _ := io.ReadAll(resp.Body)
		return nil, &UpstreamError{Status: resp.StatusCode, Body: buf}
	}
	var out LogResponse
	json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

// Tags proxies GET /_leyline/api/v1/{vault}/tags.
func (d *Daemon) Tags(prefix string) (TagsResponse, *UpstreamError) {
	base, vault, err := serverBaseAndVault(d.cfg.Vault)
	if err != nil {
		return nil, &UpstreamError{Status: 0, Body: []byte(err.Error())}
	}
	u, _ := url.Parse(fmt.Sprintf("%s/_leyline/api/v1/%s/tags", base, vault))
	if prefix != "" {
		qv := u.Query()
		qv.Set("prefix", prefix)
		u.RawQuery = qv.Encode()
	}
	req, _ := http.NewRequest("GET", u.String(), nil)
	req.Header.Set("Authorization", "Bearer "+d.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, &UpstreamError{Status: 0, Body: []byte(err.Error())}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		buf, _ := io.ReadAll(resp.Body)
		return nil, &UpstreamError{Status: resp.StatusCode, Body: buf}
	}
	var out TagsResponse
	json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}
