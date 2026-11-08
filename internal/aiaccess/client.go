package aiaccess

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// ErrOff is returned when AI access is switched off (or the app is not running).
var ErrOff = errors.New("2T5 DECK+'s AI access is off. Open 2T5 DECK+, choose the … menu at the top right, then AI access, and switch it on (the app must be running)")

// APIError is an answer the editor refused (status and the editor's own message).
type APIError struct {
	Status int
	Field  string
	Msg    string
}

func (e *APIError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s (field %s, HTTP %d)", e.Msg, e.Field, e.Status)
	}
	return fmt.Sprintf("%s (HTTP %d)", e.Msg, e.Status)
}

// Client talks to the AI door. Both the MCP server and the terminal command are thin wrappers of it.
type Client struct {
	Dir  string // state folder ("" = DefaultDir)
	HTTP *http.Client
}

func (c *Client) conn() (port int, token string, err error) {
	dir := c.Dir
	if dir == "" {
		if dir, err = DefaultDir(); err != nil {
			return 0, "", err
		}
	}
	st, err := ReadState(dir)
	if err != nil {
		return 0, "", err
	}
	if !st.Enabled || st.Port == 0 || st.Token == "" {
		return 0, "", ErrOff
	}
	return st.Port, st.Token, nil
}

func (c *Client) do(method, path string, body []byte) (int, []byte, error) {
	port, tok, err := c.conn()
	if err != nil {
		return 0, nil, err
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	req, _ := http.NewRequest(method, "http://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(port))+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, ErrOff // nothing listens: switched off, or the app is not running
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return resp.StatusCode, b, err
}

// Tools lists the tools the running app offers.
func (c *Client) Tools() ([]Tool, error) {
	code, b, err := c.do("GET", "/ai/v1/tools", nil)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, apiErr(code, b)
	}
	var r struct{ Tools []Tool }
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return r.Tools, nil
}

// Call runs one tool and returns the JSON answer.
func (c *Client) Call(tool string, arguments map[string]any) ([]byte, error) {
	body, _ := json.Marshal(map[string]any{"arguments": arguments})
	code, b, err := c.do("POST", "/ai/v1/call/"+tool, body)
	if err != nil {
		return nil, err
	}
	if code < 200 || code > 299 {
		return nil, apiErr(code, b)
	}
	return b, nil
}

func apiErr(code int, b []byte) error {
	var e struct{ Error, Field string }
	_ = json.Unmarshal(b, &e)
	if e.Error == "" {
		e.Error = string(bytes.TrimSpace(b))
	}
	return &APIError{Status: code, Field: e.Field, Msg: e.Error}
}
