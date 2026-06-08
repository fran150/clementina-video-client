package mia

import (
	"encoding/binary"
	"fmt"
)

const (
	DisplayWidth  = 320
	DisplayHeight = 200

	VideoStateSize       = 68944
	PageSize             = 32
	PageShift            = 5
	PageCount            = 2155
	FirstSyncPage        = 1
	SyncPageCount        = PageCount - FirstSyncPage
	HeaderSize           = 32
	PageRecordSize       = 34
	UDPPayloadSize       = 512
	RecordsPerChunk      = (UDPPayloadSize - HeaderSize) / PageRecordSize
	MaxFramePayloadBytes = RecordsPerChunk * PageRecordSize

	Magic   = 0x4D56
	Version = 1

	PacketHello        = 0x01
	PacketWelcome      = 0x02
	PacketRequestFrame = 0x05
	PacketAckResponse  = 0x06
	PacketNackChunks   = 0x07
	PacketFrameData    = 0x20
	PacketStatus       = 0x30

	StatusNoDirtyPages = 0
	StatusProtocolErr  = 1
)

type Header struct {
	Type       uint8
	SessionID  uint32
	Seq        uint32
	Ack        uint32
	FrameID    uint32
	RequestID  uint16
	ChunkIndex uint16
	ChunkCount uint16
	PayloadLen uint16
}

type Packet struct {
	Header  Header
	Payload []byte
}

func ParsePacket(data []byte) (Packet, error) {
	if len(data) < HeaderSize {
		return Packet{}, fmt.Errorf("short packet: %d bytes", len(data))
	}
	if binary.LittleEndian.Uint16(data[0:2]) != Magic {
		return Packet{}, fmt.Errorf("bad magic")
	}
	if data[2] != Version {
		return Packet{}, fmt.Errorf("unsupported version %d", data[2])
	}
	payloadLen := binary.LittleEndian.Uint16(data[26:28])
	if int(payloadLen) != len(data)-HeaderSize {
		return Packet{}, fmt.Errorf("payload length mismatch")
	}
	if binary.LittleEndian.Uint16(data[28:30]) != 0 || binary.LittleEndian.Uint16(data[30:32]) != 0 {
		return Packet{}, fmt.Errorf("reserved header fields are nonzero")
	}

	header := Header{
		Type:       data[3],
		SessionID:  binary.LittleEndian.Uint32(data[4:8]),
		Seq:        binary.LittleEndian.Uint32(data[8:12]),
		Ack:        binary.LittleEndian.Uint32(data[12:16]),
		FrameID:    binary.LittleEndian.Uint32(data[16:20]),
		RequestID:  binary.LittleEndian.Uint16(data[20:22]),
		ChunkIndex: binary.LittleEndian.Uint16(data[22:24]),
		ChunkCount: binary.LittleEndian.Uint16(data[24:26]),
		PayloadLen: payloadLen,
	}

	return Packet{Header: header, Payload: data[HeaderSize:]}, nil
}

func BuildPacket(header Header, payload []byte) []byte {
	header.PayloadLen = uint16(len(payload))
	packet := make([]byte, HeaderSize, HeaderSize+len(payload))
	binary.LittleEndian.PutUint16(packet[0:2], Magic)
	packet[2] = Version
	packet[3] = header.Type
	binary.LittleEndian.PutUint32(packet[4:8], header.SessionID)
	binary.LittleEndian.PutUint32(packet[8:12], header.Seq)
	binary.LittleEndian.PutUint32(packet[12:16], header.Ack)
	binary.LittleEndian.PutUint32(packet[16:20], header.FrameID)
	binary.LittleEndian.PutUint16(packet[20:22], header.RequestID)
	binary.LittleEndian.PutUint16(packet[22:24], header.ChunkIndex)
	binary.LittleEndian.PutUint16(packet[24:26], header.ChunkCount)
	binary.LittleEndian.PutUint16(packet[26:28], header.PayloadLen)

	return append(packet, payload...)
}

func StatusPayload(code uint16) []byte {
	payload := make([]byte, 2)
	binary.LittleEndian.PutUint16(payload[0:2], code)
	return payload
}
