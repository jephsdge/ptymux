package server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	LocalStreamEnvelopeAction = "stream"
	LocalStreamVersion        = 1
	LocalStreamMagic          = "PTMX"
	MaxLocalStreamDataBytes   = 64 << 10
	MaxLocalStreamErrorBytes  = 4 << 10
)

type LocalStreamFrameType byte

const (
	LocalStreamFrameStarted LocalStreamFrameType = 1
	LocalStreamFrameData    LocalStreamFrameType = 2
	LocalStreamFrameError   LocalStreamFrameType = 3
	LocalStreamFrameEnd     LocalStreamFrameType = 4
)

const localStreamHeaderBytes = len(LocalStreamMagic) + 1 + 1 + 4

func DecodeLocalRequest(reader io.Reader, request *Request) error {
	limited := &io.LimitedReader{R: reader, N: MaxLocalRequestBytes + 1}
	data, err := bufio.NewReader(limited).ReadBytes('\n')
	if len(data) > MaxLocalRequestBytes {
		return fmt.Errorf("local request exceeds %d bytes", MaxLocalRequestBytes)
	}
	if err != nil {
		return fmt.Errorf("read local request: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(request); err != nil {
		return err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("local request contains trailing JSON")
		}
		return err
	}
	return nil
}

func localStreamFrameRules(frameType LocalStreamFrameType) (maxPayload int, control bool, err error) {
	switch frameType {
	case LocalStreamFrameStarted, LocalStreamFrameEnd:
		return 0, true, nil
	case LocalStreamFrameData:
		return MaxLocalStreamDataBytes, false, nil
	case LocalStreamFrameError:
		return MaxLocalStreamErrorBytes, false, nil
	default:
		return 0, false, errors.New("unknown local stream frame type")
	}
}

func WriteLocalStreamFrame(writer io.Writer, frameType LocalStreamFrameType, payload []byte) error {
	maxPayload, control, err := localStreamFrameRules(frameType)
	if err != nil {
		return err
	}
	if control && len(payload) != 0 {
		return errors.New("local stream control frame must be empty")
	}
	if maxPayload > 0 && len(payload) > maxPayload {
		return fmt.Errorf("local stream frame exceeds %d bytes", maxPayload)
	}

	header := make([]byte, localStreamHeaderBytes)
	copy(header, LocalStreamMagic)
	header[len(LocalStreamMagic)] = LocalStreamVersion
	header[len(LocalStreamMagic)+1] = byte(frameType)
	binary.BigEndian.PutUint32(header[len(LocalStreamMagic)+2:], uint32(len(payload)))
	if err := writeAll(writer, header); err != nil {
		return err
	}
	return writeAll(writer, payload)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func ReadLocalStreamFrame(reader io.Reader) (LocalStreamFrameType, []byte, error) {
	header := make([]byte, localStreamHeaderBytes)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	if string(header[:len(LocalStreamMagic)]) != LocalStreamMagic {
		return 0, nil, errors.New("invalid local stream frame magic")
	}
	if header[len(LocalStreamMagic)] != LocalStreamVersion {
		return 0, nil, errors.New("unsupported local stream frame version")
	}
	frameType := LocalStreamFrameType(header[len(LocalStreamMagic)+1])
	length := int(binary.BigEndian.Uint32(header[len(LocalStreamMagic)+2:]))
	maxPayload, control, err := localStreamFrameRules(frameType)
	if err != nil {
		return 0, nil, err
	}
	if control && length != 0 {
		return 0, nil, errors.New("invalid local stream control frame")
	}
	if maxPayload > 0 && length > maxPayload {
		return 0, nil, fmt.Errorf("local stream frame exceeds %d bytes", maxPayload)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return frameType, payload, nil
}

type LocalStreamWriter struct {
	Writer io.Writer
}

func (w LocalStreamWriter) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		size := len(data)
		if size > MaxLocalStreamDataBytes {
			size = MaxLocalStreamDataBytes
		}
		if err := WriteLocalStreamFrame(w.Writer, LocalStreamFrameData, data[:size]); err != nil {
			return written, err
		}
		written += size
		data = data[size:]
	}
	return written, nil
}
