package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

type ippClient struct{ client *http.Client }

// Get-Printer-Attributes is read-only. An IPP fault response also proves that the
// printer is powered; lack of readiness must never authorize another long press.
func (c *ippClient) readResponse(parent context.Context, uri string) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	endpoint, err := url.Parse(uri)
	if err != nil {
		return false, fmt.Errorf("invalid IPP power probe URI")
	}
	if endpoint.Port() == "" {
		endpoint.Host = net.JoinHostPort(endpoint.Hostname(), "631")
	}
	if endpoint.Scheme == "ipps" {
		endpoint.Scheme = "https"
	} else {
		endpoint.Scheme = "http"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(buildPrinterStateRequest(uri)))
	if err != nil {
		return false, fmt.Errorf("create IPP power probe")
	}
	req.Header.Set("Content-Type", "application/ipp")
	resp, err := c.client.Do(req)
	if err != nil {
		if parent.Err() != nil {
			return false, parent.Err()
		}
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.EHOSTDOWN) || errors.Is(err, syscall.ENETUNREACH) {
			return false, nil
		}
		return false, fmt.Errorf("IPP power probe connection could not be verified")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("IPP power probe returned HTTP %d", resp.StatusCode)
	}
	var header [8]byte
	if _, err := io.ReadFull(resp.Body, header[:]); err != nil {
		return false, fmt.Errorf("IPP power probe returned an incomplete response")
	}
	if (header[0] != 1 && header[0] != 2) || binary.BigEndian.Uint32(header[4:]) != 1 {
		return false, fmt.Errorf("IPP power probe returned an invalid response")
	}
	return true, nil
}

// IPP/1.1 header, operation attributes, and end-of-attributes (RFC 8010).
func buildPrinterStateRequest(uri string) []byte {
	var body bytes.Buffer
	body.Write([]byte{1, 1, 0, 11, 0, 0, 0, 1, 1})
	for _, attribute := range []struct {
		tag         byte
		name, value string
	}{
		{0x47, "attributes-charset", "utf-8"},
		{0x48, "attributes-natural-language", "en"},
		{0x45, "printer-uri", uri},
		{0x44, "requested-attributes", "printer-state"},
	} {
		body.WriteByte(attribute.tag)
		body.WriteByte(byte(len(attribute.name) >> 8))
		body.WriteByte(byte(len(attribute.name)))
		body.WriteString(attribute.name)
		body.WriteByte(byte(len(attribute.value) >> 8))
		body.WriteByte(byte(len(attribute.value)))
		body.WriteString(attribute.value)
	}
	body.WriteByte(3)
	return body.Bytes()
}
