package main

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
)

const (
	maxLine  = 2 << 20
	maxLines = 1000
)

func send(w io.Writer, id int, method string, params any) error {
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

type response struct {
	id       int
	result   json.RawMessage
	rpcError *struct {
		Code *int `json:"code"`
	}
	detail string
}

type lineReader struct {
	mu        sync.Mutex
	buf       []byte
	lines     int
	failed    bool
	responses chan<- response
}

func (r *lineReader) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	for len(p) > 0 && !r.failed {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			r.appendPart(p)
			break
		}
		r.appendPart(p[:i])
		if !r.failed {
			r.line()
		}
		p = p[i+1:]
	}
	return n, nil
}

func (r *lineReader) appendPart(p []byte) {
	if len(r.buf)+len(p) > maxLine {
		r.fail("protocol limit")
		return
	}
	r.buf = append(r.buf, p...)
}

func (r *lineReader) line() {
	r.lines++
	if r.lines > maxLines {
		r.fail("protocol limit")
		return
	}
	line := bytes.TrimSuffix(r.buf, []byte{'\r'})
	var wire struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code *int `json:"code"`
		} `json:"error"`
	}
	item := response{
		id: wire.ID,
	}
	if json.Unmarshal(line, &wire) != nil {
		item = response{
			detail: "invalid JSON",
		}
	} else {
		item.id, item.result, item.rpcError = wire.ID, wire.Result, wire.Error
	}
	r.responses <- item
	r.buf = r.buf[:0]
}

func (r *lineReader) fail(detail string) {
	r.responses <- response{
		detail: detail,
	}
	r.failed = true
	r.buf = nil
}

func (r *lineReader) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) > 0 && !r.failed {
		r.line()
	}
}
