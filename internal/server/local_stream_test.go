package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLocalStreamFrameRoundTrip(t *testing.T) {
	payload := []byte("\x1b[31mPTMX {\"error\":\"not an error\"}\x00\xff")
	var stream bytes.Buffer
	frames := []struct {
		frameType LocalStreamFrameType
		payload   []byte
	}{
		{frameType: LocalStreamFrameStarted},
		{frameType: LocalStreamFrameData, payload: payload},
		{frameType: LocalStreamFrameError, payload: []byte("stream failed")},
		{frameType: LocalStreamFrameEnd},
	}

	for _, frame := range frames {
		if err := WriteLocalStreamFrame(&stream, frame.frameType, frame.payload); err != nil {
			t.Fatalf("WriteLocalStreamFrame(%d) returned error: %v", frame.frameType, err)
		}
	}
	for _, want := range frames {
		frameType, got, err := ReadLocalStreamFrame(&stream)
		if err != nil {
			t.Fatalf("ReadLocalStreamFrame returned error: %v", err)
		}
		if frameType != want.frameType || !bytes.Equal(got, want.payload) {
			t.Fatalf("frame = (%d, %q), want (%d, %q)", frameType, got, want.frameType, want.payload)
		}
	}
}

func TestLocalStreamWriterSplitsLargePayload(t *testing.T) {
	data := bytes.Repeat([]byte("x"), MaxLocalStreamDataBytes*2+17)
	var stream bytes.Buffer
	writer := LocalStreamWriter{Writer: &stream}

	n, err := writer.Write(data)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(data) {
		t.Fatalf("Write returned %d, want %d", n, len(data))
	}

	var got bytes.Buffer
	var sizes []int
	for stream.Len() > 0 {
		frameType, payload, err := ReadLocalStreamFrame(&stream)
		if err != nil {
			t.Fatal(err)
		}
		if frameType != LocalStreamFrameData {
			t.Fatalf("frame type = %d, want data", frameType)
		}
		sizes = append(sizes, len(payload))
		got.Write(payload)
	}
	wantSizes := []int{MaxLocalStreamDataBytes, MaxLocalStreamDataBytes, 17}
	if len(sizes) != len(wantSizes) {
		t.Fatalf("frame sizes = %v, want %v", sizes, wantSizes)
	}
	for i := range sizes {
		if sizes[i] != wantSizes[i] {
			t.Fatalf("frame sizes = %v, want %v", sizes, wantSizes)
		}
	}
	if !bytes.Equal(got.Bytes(), data) {
		t.Fatal("reassembled data differs from input")
	}
}

func TestLocalStreamFrameRejectsInvalidFrames(t *testing.T) {
	if err := WriteLocalStreamFrame(io.Discard, LocalStreamFrameStarted, []byte("x")); err == nil {
		t.Fatal("control frame with payload was accepted")
	}
	if err := WriteLocalStreamFrame(io.Discard, LocalStreamFrameData, make([]byte, MaxLocalStreamDataBytes+1)); err == nil {
		t.Fatal("oversized data frame was accepted")
	}
	if err := WriteLocalStreamFrame(io.Discard, LocalStreamFrameType(99), nil); err == nil {
		t.Fatal("unknown frame type was accepted")
	}

	tests := []struct {
		name   string
		header func() []byte
	}{
		{name: "magic", header: func() []byte {
			header := validLocalStreamHeader(LocalStreamFrameEnd, 0)
			copy(header, "NOPE")
			return header
		}},
		{name: "version", header: func() []byte {
			header := validLocalStreamHeader(LocalStreamFrameEnd, 0)
			header[len(LocalStreamMagic)]++
			return header
		}},
		{name: "unknown type", header: func() []byte {
			return validLocalStreamHeader(LocalStreamFrameType(99), 0)
		}},
		{name: "control payload", header: func() []byte {
			return validLocalStreamHeader(LocalStreamFrameStarted, 1)
		}},
		{name: "oversized data", header: func() []byte {
			return validLocalStreamHeader(LocalStreamFrameData, MaxLocalStreamDataBytes+1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := ReadLocalStreamFrame(bytes.NewReader(test.header())); err == nil {
				t.Fatal("invalid frame was accepted")
			}
		})
	}

	truncated := append(validLocalStreamHeader(LocalStreamFrameData, 4), []byte("abc")...)
	if _, _, err := ReadLocalStreamFrame(bytes.NewReader(truncated)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated frame error = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestDecodeLocalRequestBoundsAndStrictness(t *testing.T) {
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(Request{Action: "list"}); err != nil {
		t.Fatal(err)
	}
	var request Request
	if err := DecodeLocalRequest(&encoded, &request); err != nil {
		t.Fatal(err)
	}
	if request.Action != "list" {
		t.Fatalf("Action = %q, want list", request.Action)
	}

	if err := DecodeLocalRequest(strings.NewReader("{\"action\":\"list\",\"extra\":true}\n"), &Request{}); err == nil {
		t.Fatal("unknown field was accepted")
	}
	if err := DecodeLocalRequest(strings.NewReader("{\"action\":\"list\"} {}\n"), &Request{}); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
	oversized := strings.Repeat("x", MaxLocalRequestBytes+1) + "\n"
	if err := DecodeLocalRequest(strings.NewReader(oversized), &Request{}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized request error = %v", err)
	}
}

func TestDaemonFramedStreamSeparatesDataAndError(t *testing.T) {
	streamData := []byte("\x1b[32mPTMX {\"error\":\"terminal text\"}\x00\xff")
	streamErr := errors.New("runner stream failed")
	runner := &framedStreamRunner{fakeRunner: &fakeRunner{}, output: streamData, err: streamErr}
	daemon := NewDaemon("")
	daemon.service = newServiceWithRunner(func() Runner { return runner })
	daemon.store = daemon.service.Store()
	if resp := daemon.service.Create(Request{Session: "work", Pane: "main", Tab: "shell"}); resp.Error != "" {
		t.Fatal(resp.Error)
	}

	conn := newStreamTestConn(nil)
	daemon.handleFramedStream(conn, Request{
		Action:        LocalStreamEnvelopeAction,
		Session:       "work",
		Pane:          "main",
		Tab:           "shell",
		StreamVersion: LocalStreamVersion,
		StreamAction:  "follow",
	})

	reader := bytes.NewReader([]byte(conn.String()))
	assertLocalStreamFrame(t, reader, LocalStreamFrameStarted, nil)
	assertLocalStreamFrame(t, reader, LocalStreamFrameData, streamData)
	assertLocalStreamFrame(t, reader, LocalStreamFrameError, []byte(streamErr.Error()))
	assertLocalStreamFrame(t, reader, LocalStreamFrameEnd, nil)
	if reader.Len() != 0 {
		t.Fatalf("unexpected trailing framed bytes: %d", reader.Len())
	}
}

func TestDaemonFramedStreamReportsInvalidRequestAsErrorFrame(t *testing.T) {
	daemon := NewDaemon("")
	conn := newStreamTestConn(nil)
	daemon.handleFramedStream(conn, Request{
		Action:        LocalStreamEnvelopeAction,
		Session:       "work",
		Pane:          "main",
		Tab:           "shell",
		Command:       "alt-x",
		StreamVersion: LocalStreamVersion,
		StreamAction:  "keys",
	})

	reader := bytes.NewReader([]byte(conn.String()))
	assertLocalStreamFrame(t, reader, LocalStreamFrameStarted, nil)
	frameType, payload, err := ReadLocalStreamFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	if frameType != LocalStreamFrameError || len(payload) == 0 {
		t.Fatalf("frame = (%d, %q), want non-empty error frame", frameType, payload)
	}
	assertLocalStreamFrame(t, reader, LocalStreamFrameEnd, nil)
	if got := daemon.store.Snapshot(); len(got.Sessions) != 0 {
		t.Fatalf("snapshot = %+v, want no created targets", got)
	}
}

func validLocalStreamHeader(frameType LocalStreamFrameType, length int) []byte {
	header := make([]byte, localStreamHeaderBytes)
	copy(header, LocalStreamMagic)
	header[len(LocalStreamMagic)] = LocalStreamVersion
	header[len(LocalStreamMagic)+1] = byte(frameType)
	binary.BigEndian.PutUint32(header[len(LocalStreamMagic)+2:], uint32(length))
	return header
}

func assertLocalStreamFrame(t *testing.T, reader io.Reader, wantType LocalStreamFrameType, wantPayload []byte) {
	t.Helper()
	frameType, payload, err := ReadLocalStreamFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	if frameType != wantType || !bytes.Equal(payload, wantPayload) {
		t.Fatalf("frame = (%d, %q), want (%d, %q)", frameType, payload, wantType, wantPayload)
	}
}

type framedStreamRunner struct {
	*fakeRunner
	output []byte
	err    error
}

func (r *framedStreamRunner) Follow(output io.Writer, _ <-chan struct{}) error {
	if _, err := output.Write(r.output); err != nil {
		return err
	}
	return r.err
}
