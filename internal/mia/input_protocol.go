package mia

import (
	"encoding/binary"
	"fmt"
)

const (
	InputDefaultPort = 6503

	InputMagic   = "MIIN"
	InputVersion = 1

	InputHeaderSize = 12

	InputPacketHello        = 0x01
	InputPacketWelcome      = 0x02
	InputPacketDisconnect   = 0x04
	InputPacketText         = 0x10
	InputPacketHIDEvent     = 0x11
	InputPacketHIDBitmap    = 0x12
	InputPacketMouseDelta   = 0x20
	InputPacketGamepadState = 0x30
	InputPacketGamepadClear = 0x31
	InputPacketClearState   = 0x40

	InputWelcomeAccepted           = 0x00
	InputWelcomeBusy               = 0x01
	InputWelcomeUnsupportedVersion = 0x02

	InputCapText     = 1 << 0
	InputCapKeyboard = 1 << 1
	InputCapConsumer = 1 << 2
	InputCapMouse    = 1 << 3
	InputCapGamepad  = 1 << 4

	HIDPageKeyboard = 0x0007
	HIDPageConsumer = 0x000C

	HIDEventDown   = 1 << 0
	HIDEventRepeat = 1 << 1

	ClearText     = 1 << 0
	ClearKeyboard = 1 << 1
	ClearConsumer = 1 << 2
	ClearMouse    = 1 << 3
	ClearGamepads = 1 << 4
	ClearAll      = 1 << 7
)

type InputHeader struct {
	Type    uint8
	Seq     uint16
	Session uint32
}

type InputPacket struct {
	Header  InputHeader
	Payload []byte
}

func ParseInputPacket(data []byte) (InputPacket, error) {
	if len(data) < InputHeaderSize {
		return InputPacket{}, fmt.Errorf("short packet: %d bytes", len(data))
	}
	if string(data[0:4]) != InputMagic {
		return InputPacket{}, fmt.Errorf("bad magic")
	}
	if data[4] != InputVersion {
		return InputPacket{}, fmt.Errorf("unsupported version %d", data[4])
	}

	return InputPacket{
		Header: InputHeader{
			Type:    data[5],
			Seq:     binary.LittleEndian.Uint16(data[6:8]),
			Session: binary.LittleEndian.Uint32(data[8:12]),
		},
		Payload: data[InputHeaderSize:],
	}, nil
}

func BuildInputPacket(header InputHeader, payload []byte) []byte {
	packet := make([]byte, InputHeaderSize, InputHeaderSize+len(payload))
	copy(packet[0:4], InputMagic)
	packet[4] = InputVersion
	packet[5] = header.Type
	binary.LittleEndian.PutUint16(packet[6:8], header.Seq)
	binary.LittleEndian.PutUint32(packet[8:12], header.Session)
	return append(packet, payload...)
}

func InputHelloPayload(capabilities uint16, name string) []byte {
	if len(name) > 255 {
		name = name[:255]
	}
	payload := make([]byte, 3+len(name))
	binary.LittleEndian.PutUint16(payload[0:2], capabilities)
	payload[2] = uint8(len(name))
	copy(payload[3:], name)
	return payload
}

func InputTextPayload(text []byte) []byte {
	payload := make([]byte, 1+len(text))
	payload[0] = uint8(len(text))
	copy(payload[1:], text)
	return payload
}

func InputHIDEventPayload(usagePage uint16, usageID uint16, flags uint8, text uint8) []byte {
	payload := make([]byte, 6)
	binary.LittleEndian.PutUint16(payload[0:2], usagePage)
	binary.LittleEndian.PutUint16(payload[2:4], usageID)
	payload[4] = flags
	payload[5] = text
	return payload
}

func InputHIDBitmapPayload(usagePage uint16, bitmap [32]byte) []byte {
	payload := make([]byte, 34)
	binary.LittleEndian.PutUint16(payload[0:2], usagePage)
	copy(payload[2:], bitmap[:])
	return payload
}

func InputMouseDeltaPayload(buttons uint8, dx int8, dy int8, wheel int8, pan int8) []byte {
	return []byte{buttons, byte(dx), byte(dy), byte(wheel), byte(pan)}
}

func InputGamepadStatePayload(player uint8, state [10]byte) []byte {
	payload := make([]byte, 11)
	payload[0] = player
	copy(payload[1:], state[:])
	return payload
}
