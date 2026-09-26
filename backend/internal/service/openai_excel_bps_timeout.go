package service

import (
	"errors"
	"io"
	"sync/atomic"
	"time"
)

var errExcelBPSRequestTimeout = errors.New("excel BPS total request timeout")

// Count raw upstream bytes, not local keepalive writes or only bridged events.
type excelBPSActivityBody struct {
	io.ReadCloser
	lastRead atomic.Int64
}

func (b *excelBPSActivityBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.lastRead.Store(time.Now().UnixNano())
	}
	return n, err
}
