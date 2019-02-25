// BSD 3-Clause License
//
// Copyright (c) 2019, Guillaume Ballet
// All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are met:
//
// * Redistributions of source code must retain the above copyright notice, this
//   list of conditions and the following disclaimer.
//
// * Redistributions in binary form must reproduce the above copyright notice,
//   this list of conditions and the following disclaimer in the documentation
//   and/or other materials provided with the distribution.
//
// * Neither the name of the copyright holder nor the names of its
//   contributors may be used to endorse or promote products derived from
//   this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
// AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
// IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
// DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
// FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
// DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
// SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
// CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
// OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

package pcsc

import (
	"encoding/binary"
	"fmt"
	"net"
	"unsafe"
)

type PCSCDClient struct {
	conn net.Conn

	minor uint32
	major uint32

	ctx uint32

	readerStateDescriptors [MaxReaderStateDescriptors]readerState
}

func New() *PCSCDClient {
	return &PCSCDClient{}
}

func (client *PCSCDClient) SCardEstablishContext(scope uint32) error {
	conn, err := clientSetupSession()
	if err != nil {
		return err
	}
	client.conn = conn

	/* Exchange version information */
	payload := make([]byte, 12)
	binary.LittleEndian.PutUint32(payload, ProtocolVersionMajor)
	binary.LittleEndian.PutUint32(payload[4:], ProtocolVersionMinor)
	binary.LittleEndian.PutUint32(payload[8:], SCardSuccess)
	err = messageSendWithHeader(CommandVersion, conn, payload)
	if err != nil {
		return err
	}
	response := make([]byte, 12)
	n, err := conn.Read(response)
	if err != nil {
		return err
	}
	if n != len(response) {
		return fmt.Errorf("invalid response length: expected %d, got %d", len(response), n)
	}
	code := binary.LittleEndian.Uint32(response[8:])
	if code != SCardSuccess {
		return fmt.Errorf("invalid response code: expected %d, got %d", SCardSuccess, code)
	}
	client.major = binary.LittleEndian.Uint32(response)
	client.minor = binary.LittleEndian.Uint32(response[4:])
	if client.major != ProtocolVersionMajor || client.minor != ProtocolVersionMinor {
		return fmt.Errorf("invalid version found: expected %d.%d, got %d.%d", ProtocolVersionMajor, ProtocolVersionMinor, client.major, client.minor)
	}

	/* Establish the context proper */
	binary.LittleEndian.PutUint32(payload, scope)
	binary.LittleEndian.PutUint32(payload[4:], 0)
	binary.LittleEndian.PutUint32(payload[8:], SCardSuccess)
	err = messageSendWithHeader(SCardEstablishContext, conn, payload)
	if err != nil {
		return err
	}
	response = make([]byte, 12)
	n, err = conn.Read(response)
	if err != nil {
		return err
	}
	if n != len(response) {
		return fmt.Errorf("invalid response length: expected %d, got %d", len(response), n)
	}
	code = binary.LittleEndian.Uint32(response[8:])
	if code != SCardSuccess {
		return fmt.Errorf("invalid response code: expected %d, got %d", SCardSuccess, code)
	}
	client.ctx = binary.LittleEndian.Uint32(response[4:])

	return nil
}

func (client *PCSCDClient) SCardReleaseContext() error {
	data := [8]byte{}
	binary.LittleEndian.PutUint32(data[:], client.ctx)
	binary.LittleEndian.PutUint32(data[4:], SCardSuccess)
	err := messageSendWithHeader(SCardReleaseContext, client.conn, data[:])
	if err != nil {
		return err
	}
	total := 0
	for total < len(data) {
		n, err := client.conn.Read(data[total:])
		if err != nil {
			return err
		}
		total += n
	}
	code := binary.LittleEndian.Uint32(data[4:])
	if code != SCardSuccess {
		return fmt.Errorf("invalid return code: %x", code)
	}

	return nil
}

// Constants related to the reader state structure
const (
	ReaderStateNameLength       = 128
	ReaderStateMaxAtrSizeLength = 33
	// NOTE: ATR is 32-byte aligned in the C version, which means it's
	// actually 36 byte long and not 33.
	ReaderStateDescriptorLength = ReaderStateNameLength + ReaderStateMaxAtrSizeLength + 5*4 + 3

	MaxReaderStateDescriptors = 16
)

type readerState struct {
	name          string /* reader name */
	eventCounter  uint32 /* number of card events */
	readerState   uint32 /* SCARD_* bit field */
	readerSharing uint32 /* PCSCLITE_SHARING_* sharing status */

	cardAtr       [ReaderStateMaxAtrSizeLength]byte /* ATR */
	cardAtrLength uint32                            /* ATR length */
	cardProtocol  uint32                            /* SCARD_PROTOCOL_* value */
}

func getReaderState(data []byte) (readerState, error) {
	ret := readerState{}
	if len(data) < ReaderStateDescriptorLength {
		return ret, fmt.Errorf("could not unmarshall data of length %d < %d", len(data), ReaderStateDescriptorLength)
	}

	ret.name = string(data[:ReaderStateNameLength])
	ret.eventCounter = binary.LittleEndian.Uint32(data[unsafe.Offsetof(ret.eventCounter):])
	ret.readerState = binary.LittleEndian.Uint32(data[unsafe.Offsetof(ret.readerState):])
	ret.readerSharing = binary.LittleEndian.Uint32(data[unsafe.Offsetof(ret.readerSharing):])
	copy(ret.cardAtr[:], data[unsafe.Offsetof(ret.cardAtr):unsafe.Offsetof(ret.cardAtr)+ReaderStateMaxAtrSizeLength])
	ret.cardAtrLength = binary.LittleEndian.Uint32(data[unsafe.Offsetof(ret.cardAtrLength):])
	ret.cardProtocol = binary.LittleEndian.Uint32(data[unsafe.Offsetof(ret.cardProtocol):])

	return ret, nil
}

// SCardListReaders gets the list of readers from the daemon
func (client *PCSCDClient) SCardListReaders() error {
	err := messageSendWithHeader(CommandGetReaderState, client.conn, []byte{})
	if err != nil {
		return err
	}
	response := make([]byte, ReaderStateDescriptorLength*MaxReaderStateDescriptors)
	total := 0
	for total < len(response) {
		n, err := client.conn.Read(response[total:])
		if err != nil {
			return err
		}
		total += n
	}

	for i := range client.readerStateDescriptors {
		desc, err := getReaderState(response[i*ReaderStateDescriptorLength:])
		if err != nil {
			return err
		}
		client.readerStateDescriptors[i] = desc
	}

	return nil
}
