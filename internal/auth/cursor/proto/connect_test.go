package proto

import (
	"bytes"
	"errors"
	"testing"
)

func TestConnectFrameRoundTrip(t *testing.T) {
	payload := []byte("hello-cursor")
	frame := FrameConnectMessage(payload, 0)
	if len(frame) != ConnectFrameHeaderSize+len(payload) {
		t.Fatalf("frame length = %d, want %d", len(frame), ConnectFrameHeaderSize+len(payload))
	}

	flags, got, consumed, ok := ParseConnectFrame(frame)
	if !ok {
		t.Fatal("ParseConnectFrame failed on complete frame")
	}
	if flags != 0 {
		t.Errorf("flags = %#x, want 0", flags)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload = %q, want %q", got, payload)
	}
	if consumed != len(frame) {
		t.Errorf("consumed = %d, want %d", consumed, len(frame))
	}
}

func TestParseConnectFrameIncomplete(t *testing.T) {
	if _, _, _, ok := ParseConnectFrame([]byte{0, 0, 0}); ok {
		t.Error("short header should not parse")
	}
	frame := FrameConnectMessage([]byte("0123456789"), 0)
	if _, _, _, ok := ParseConnectFrame(frame[:len(frame)-2]); ok {
		t.Error("truncated payload should not parse")
	}
}

func TestParseConnectFrameBackToBack(t *testing.T) {
	first := FrameConnectMessage([]byte("aa"), 0)
	second := FrameConnectMessage([]byte("bbb"), ConnectEndStreamFlag)
	buf := append(first, second...)

	_, p1, n1, ok1 := ParseConnectFrame(buf)
	if !ok1 || string(p1) != "aa" {
		t.Fatalf("first frame parse failed: ok=%v payload=%q", ok1, p1)
	}
	flags2, p2, n2, ok2 := ParseConnectFrame(buf[n1:])
	if !ok2 {
		t.Fatalf("second frame parse failed")
	}
	if flags2&ConnectEndStreamFlag == 0 {
		t.Errorf("second frame missing end-stream flag: %#x", flags2)
	}
	if string(p2) != "bbb" || n2 != len(second) {
		t.Errorf("second frame payload=%q consumed=%d", p2, n2)
	}
}

func TestParseConnectEndStream(t *testing.T) {
	if err := ParseConnectEndStream(nil); err != nil {
		t.Errorf("empty trailer: %v", err)
	}
	if err := ParseConnectEndStream([]byte(`{}`)); err != nil {
		t.Errorf("error-less trailer: %v", err)
	}
	err := ParseConnectEndStream([]byte(`{"error":{"code":"resource_exhausted","message":"quota exceeded"}}`))
	var ce *ConnectError
	if !errors.As(err, &ce) {
		t.Fatalf("error trailer not a ConnectError: %v", err)
	}
	if ce.Code != "resource_exhausted" || ce.Message != "quota exceeded" {
		t.Errorf("ConnectError = %+v", ce)
	}
}

func TestEncodeHeartbeat(t *testing.T) {
	hb := EncodeHeartbeat()
	if len(hb) == 0 {
		t.Fatal("heartbeat encoding is empty")
	}
}
