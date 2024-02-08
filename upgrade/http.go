// HTTP-related effects.
package upgrade

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Use this function in steps to perform HTTP GET.
func getHTTP(ctx context.Context, url string) ([]byte, error) {
	h, ok := ctx.Value(httpHandlerKey).(httpHandler)
	if !ok {
		h = &defaultHttpHandler{
			retryAttempts: 5,
			delay: func(attempt int) time.Duration {
				n := (attempt + 1) * (attempt + 1)
				return time.Duration(n) * time.Second
			},
		}
	}
	return h.getHTTP(url)
}

// Set this to a mock in tests to avoid hitting actual HTTP.
type httpHandler interface {
	getHTTP(url string) ([]byte, error)
}

var httpHandlerKey = struct{}{}

type defaultHttpHandler struct {
	retryAttempts int
	delay         func(attempt int) time.Duration
}

func (*defaultHttpHandler) tryGetHTTP(url string) (content []byte, finalError error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil && finalError == nil {
			finalError = err
		}
	}()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Non-200 code for GET %v: %v", url, resp.StatusCode)
	}
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := resp.Body.Close(); err != nil {
		return nil, err
	}
	return respBytes, nil
}

func (h *defaultHttpHandler) getHTTP(url string) ([]byte, error) {
	var c []byte
	var err error
	for attempt := 0; attempt < h.retryAttempts; attempt++ {
		c, err = h.tryGetHTTP(url)
		if err == nil {
			return c, nil
		}
		time.Sleep(h.delay(attempt))
	}
	return nil, err
}
