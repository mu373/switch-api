package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildPrinterStateRequest(t *testing.T) {
	body := buildPrinterStateRequest("ipp://printer:631/ipp/print")
	if !bytes.Equal(body[:9], []byte{1, 1, 0, 11, 0, 0, 0, 1, 1}) || body[len(body)-1] != 3 {
		t.Fatalf("invalid read-only IPP request: %x", body)
	}
	remainder := body[9 : len(body)-1]
	for _, attribute := range []struct {
		tag         byte
		name, value string
	}{
		{0x47, "attributes-charset", "utf-8"}, {0x48, "attributes-natural-language", "en"},
		{0x45, "printer-uri", "ipp://printer:631/ipp/print"}, {0x44, "requested-attributes", "printer-state"},
	} {
		if remainder[0] != attribute.tag {
			t.Fatal("incorrect IPP value tag")
		}
		nameLength := int(binary.BigEndian.Uint16(remainder[1:3]))
		name := string(remainder[3 : 3+nameLength])
		remainder = remainder[3+nameLength:]
		valueLength := int(binary.BigEndian.Uint16(remainder[:2]))
		value := string(remainder[2 : 2+valueLength])
		remainder = remainder[2+valueLength:]
		if name != attribute.name || value != attribute.value {
			t.Fatalf("attribute %q=%q", name, value)
		}
	}
	if len(remainder) != 0 {
		t.Fatal("unexpected extra IPP attributes")
	}
}

func TestIPPResponseConfirmsPowerEvenForPrinterFault(t *testing.T) {
	for _, test := range []struct {
		name       string
		body       []byte
		httpStatus int
		wantError  bool
	}{
		{"ready", []byte{1, 1, 0, 0, 0, 0, 0, 1, 3}, 200, false},
		{"printer fault still powered", []byte{2, 0, 5, 0, 0, 0, 0, 1, 3}, 200, false},
		{"wrong request ID", []byte{1, 1, 0, 0, 0, 0, 0, 2}, 200, true},
		{"incomplete header", []byte{1, 1, 0}, 200, true},
		{"not IPP", []byte("not an IPP response"), 200, true},
		{"authentication error", nil, 401, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/ipp" {
					t.Error("not an IPP request")
				}
				body, _ := io.ReadAll(r.Body)
				if !bytes.Equal(body[:4], []byte{1, 1, 0, 11}) {
					t.Error("probe must not print or mutate")
				}
				w.WriteHeader(test.httpStatus)
				_, _ = w.Write(test.body)
			}))
			defer server.Close()
			responsive, err := (&ippClient{client: server.Client()}).readResponse(context.Background(), strings.Replace(server.URL, "http:", "ipp:", 1)+"/ipp/print")
			if (err != nil) != test.wantError || responsive == test.wantError {
				t.Fatalf("responsive=%v, err=%v", responsive, err)
			}
		})
	}
}

func TestIPPConnectionRefusedDoesNotConfirmPower(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	uri := strings.Replace(server.URL, "http:", "ipp:", 1) + "/ipp/print"
	server.Close()
	responsive, err := (&ippClient{client: server.Client()}).readResponse(context.Background(), uri)
	if responsive || err != nil {
		t.Fatalf("responsive=%v, err=%v", responsive, err)
	}
}
