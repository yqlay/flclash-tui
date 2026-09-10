//go:build linux && !cgo && cli

package subscription

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

func Fetch(value string) ([]byte, error) {
	payload, err := FetchDetails(value)
	return payload.Data, err
}

func FetchDetails(value string) (Payload, error) {
	request, err := NewRequest(value)
	if err != nil {
		return Payload{}, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return Payload{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Payload{}, fmt.Errorf("subscription returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxBytes+1))
	if err != nil {
		return Payload{}, err
	}
	if len(data) == 0 {
		return Payload{}, errors.New("subscription response is empty")
	}
	if len(data) > MaxBytes {
		return Payload{}, fmt.Errorf(
			"subscription response exceeds %d MiB",
			MaxBytes>>20,
		)
	}
	payload, err := Normalize(data)
	if err != nil {
		return Payload{}, fmt.Errorf(
			"downloaded subscription is invalid: %w",
			err,
		)
	}
	payload.FileName = NewFileName(
		response.Header.Get("Content-Disposition"),
	)
	return payload, nil
}

func NewRequest(value string) (*http.Request, error) {
	request, err := http.NewRequest(http.MethodGet, value, nil)
	if err != nil ||
		(request.URL.Scheme != "http" && request.URL.Scheme != "https") {
		return nil, errors.New("subscription URL must use http or https")
	}
	request.Header.Set("User-Agent", UserAgent)
	request.Header.Set(
		"Accept",
		"application/yaml, application/x-yaml, application/json, text/yaml, text/plain, */*",
	)
	return request, nil
}
